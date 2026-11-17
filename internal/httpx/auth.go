package httpx

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"gocurl/internal/config"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Azure/go-ntlmssp"
	http "github.com/bogdanfinn/fhttp"
)

func CredentialsForURL(o *config.Options, u *url.URL) (string, string, bool) {
	if o.User != "" {
		user, pass, _ := strings.Cut(o.User, ":")
		return user, pass, true
	}
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		return user, pass, true
	}
	if o.Netrc || o.NetrcFile != "" {
		return netrcCredentials(o, u.Hostname())
	}
	return "", "", false
}

type netrcCred struct {
	login    string
	password string
}

func netrcCredentials(o *config.Options, host string) (string, string, bool) {
	path := o.NetrcFile
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", false
		}
		path = filepath.Join(home, ".netrc")
	}
	data, err := config.ReadFile(path)
	if err != nil {
		return "", "", false
	}

	machines := make(map[string]*netrcCred)
	var defaultCred *netrcCred
	var current *netrcCred
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		for i := 0; i < len(fields); i++ {
			switch strings.ToLower(fields[i]) {
			case "machine":
				if i+1 < len(fields) {
					current = &netrcCred{}
					machines[normalizeNetrcHost(fields[i+1])] = current
					i++
				}
			case "default":
				defaultCred = &netrcCred{}
				current = defaultCred
			case "login":
				if i+1 < len(fields) && current != nil {
					current.login = fields[i+1]
					i++
				}
			case "password":
				if i+1 < len(fields) && current != nil {
					current.password = fields[i+1]
					i++
				}
			case "account":
				if i+1 < len(fields) {
					i++
				}
			case "macdef":
				for scanner.Scan() && strings.TrimSpace(scanner.Text()) != "" {
				}
				i = len(fields)
			}
		}
	}
	if scanner.Err() != nil {
		return "", "", false
	}
	cred := machines[normalizeNetrcHost(host)]
	if cred == nil {
		cred = defaultCred
	}
	if cred == nil || cred.login == "" {
		return "", "", false
	}
	return cred.login, cred.password, true
}

func normalizeNetrcHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func applyBasicAuth(req *http.Request, order []string, user, pass string) []string {
	req.SetBasicAuth(user, pass)
	return HeaderOrderInsertBefore(order, "authorization", "accept-encoding")
}

func InitialNTLMHeader(o *config.Options, user, pass string) (string, int) {
	if o.AuthMode != "ntlm" && o.AuthMode != "negotiate" && o.AuthMode != "anyauth" {
		return "", 0
	}
	_, domain, _ := ntlmssp.GetDomain(user)
	msg, err := ntlmssp.NewNegotiateMessage(domain, "")
	if err != nil {
		return "", 0
	}
	scheme := "NTLM"
	if o.AuthMode == "negotiate" {
		scheme = "Negotiate"
	}
	return scheme + " " + base64.StdEncoding.EncodeToString(msg), 1
}

type authChallenge struct {
	scheme string
	params map[string]string
	token  string
}

func parseAuthChallenges(resp *http.Response) []authChallenge {
	var out []authChallenge
	for name, values := range resp.Header {
		if !strings.EqualFold(name, "WWW-Authenticate") {
			continue
		}
		for _, value := range values {
			out = append(out, parseAuthChallenge(value)...)
		}
	}
	return out
}

func parseAuthChallenge(value string) []authChallenge {
	var out []authChallenge
	for pos := 0; pos < len(value); {
		pos = skipAuthSeparators(value, pos)
		if pos >= len(value) {
			break
		}
		scheme, after := scanAuthToken(value, pos)
		if scheme == "" {
			pos = nextAuthComma(value, pos)
			if pos < len(value) {
				pos++
			}
			continue
		}
		lowerScheme := asciiLower(scheme)
		if !isSupportedAuthScheme(lowerScheme) {
			pos = findNextAuthChallenge(value, after)
			continue
		}
		end := findNextAuthChallenge(value, after)
		part := strings.TrimSpace(value[after:end])
		challenge := authChallenge{scheme: strings.ToUpper(lowerScheme)}
		if lowerScheme == "digest" {
			challenge.params = parseDigestParams(part)
		} else {
			challenge.token = strings.TrimSpace(part)
		}
		out = append(out, challenge)
		pos = end
	}
	return out
}

func asciiLower(value string) string {
	out := []byte(value)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}

func isSupportedAuthScheme(scheme string) bool {
	switch scheme {
	case "digest", "basic", "ntlm", "negotiate":
		return true
	default:
		return false
	}
}

func isAuthTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func scanAuthToken(value string, start int) (string, int) {
	pos := start
	for pos < len(value) && isAuthTokenChar(value[pos]) {
		pos++
	}
	if pos == start {
		return "", start
	}
	return value[start:pos], pos
}

func skipAuthWhitespace(value string, pos int) int {
	for pos < len(value) && (value[pos] == ' ' || value[pos] == '\t') {
		pos++
	}
	return pos
}

func skipAuthSeparators(value string, pos int) int {
	for pos < len(value) {
		pos = skipAuthWhitespace(value, pos)
		if pos >= len(value) || value[pos] != ',' {
			return pos
		}
		pos++
	}
	return pos
}

func nextAuthComma(value string, start int) int {
	quoted := false
	escaped := false
	for i := start; i < len(value); i++ {
		c := value[i]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case ',':
			return i
		}
	}
	return len(value)
}

func findNextAuthChallenge(value string, start int) int {
	quoted := false
	escaped := false
	for i := start; i < len(value); i++ {
		c := value[i]
		if quoted {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case ',':
			next := skipAuthWhitespace(value, i+1)
			token, after := scanAuthToken(value, next)
			if token == "" || !isSupportedAuthScheme(asciiLower(token)) {
				continue
			}
			after = skipAuthWhitespace(value, after)
			if after >= len(value) || value[after] != '=' {
				return i
			}
		}
	}
	return len(value)
}

func parseDigestParams(value string) map[string]string {
	params := make(map[string]string)
	for _, part := range splitHeaderParams(value) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(val[1 : len(val)-1])
		}
		params[strings.ToLower(strings.TrimSpace(key))] = val
	}
	return params
}

func splitHeaderParams(value string) []string {
	var parts []string
	start := 0
	quoted := false
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			if quoted {
				i++
			}
		case '"':
			quoted = !quoted
		case ',':
			if !quoted {
				parts = append(parts, value[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, value[start:])
}

func NextAuthHeader(o *config.Options, req *http.Request, resp *http.Response, body []byte, stage int, user, pass string) (string, int, bool, error) {
	challenges := parseAuthChallenges(resp)
	if len(challenges) == 0 {
		return "", stage, false, nil
	}
	mode := o.AuthMode
	if mode == "" || mode == "basic" {
		return "", stage, false, nil
	}
	choose := func(name string) (authChallenge, bool) {
		for _, challenge := range challenges {
			if challenge.scheme == name {
				return challenge, true
			}
		}
		return authChallenge{}, false
	}
	if mode == "digest" || mode == "anyauth" {
		if challenge, ok := choose("DIGEST"); ok {
			header, err := digestAuthorization(req, challenge.params, user, pass)
			if err == nil {
				return header, stage + 1, true, nil
			}
			return "", stage, false, err
		}
	}
	if mode == "ntlm" || mode == "negotiate" || mode == "anyauth" {
		scheme := "NTLM"
		if mode == "negotiate" {
			scheme = "Negotiate"
		}
		challenge, ok := choose("NTLM")
		if !ok && mode == "negotiate" {
			challenge, ok = choose("NEGOTIATE")
		}
		if ok {
			scheme = challenge.scheme
			if challenge.token == "" || stage == 0 {
				header, next := InitialNTLMHeader(o, user, pass)
				if header != "" {
					return strings.Replace(header, "NTLM ", scheme+" ", 1), next, true, nil
				}
			}
			raw, err := base64.StdEncoding.DecodeString(challenge.token)
			if err == nil {
				_, _, domainNeeded := ntlmssp.GetDomain(user)
				msg, err := ntlmssp.ProcessChallenge(raw, user, pass, domainNeeded)
				if err == nil {
					return scheme + " " + base64.StdEncoding.EncodeToString(msg), stage + 1, true, nil
				}
			}
		}
	}
	if mode == "anyauth" {
		if _, ok := choose("BASIC"); ok {
			token := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
			return "Basic " + token, stage + 1, true, nil
		}
	}
	return "", stage, false, nil
}

func digestAuthorization(req *http.Request, params map[string]string, user, pass string) (string, error) {
	realm := params["realm"]
	nonce := params["nonce"]
	if nonce == "" {
		return "", fmt.Errorf("digest challenge without nonce")
	}
	algorithm := strings.ToUpper(params["algorithm"])
	if algorithm == "" {
		algorithm = "MD5"
	}
	hash := digestHash(algorithm)
	if hash == nil {
		return "", fmt.Errorf("unsupported digest algorithm %q", algorithm)
	}
	uri := req.URL.RequestURI()
	cnonceBytes := make([]byte, 16)
	_, _ = rand.Read(cnonceBytes)
	cnonce := hex.EncodeToString(cnonceBytes)
	nc, err := nextNonceCount(req.URL, realm, nonce)
	if err != nil {
		return "", err
	}
	ha1 := hashHex(hash, user+":"+realm+":"+pass)
	if strings.HasSuffix(algorithm, "-SESS") {
		ha1 = hashHex(hash, ha1+":"+nonce+":"+cnonce)
	}
	qop := ""
	for _, item := range strings.Split(params["qop"], ",") {
		item = strings.TrimSpace(item)
		if item == "auth" {
			qop = "auth"
			break
		}
		if item == "auth-int" && qop == "" {
			qop = "auth-int"
		}
	}
	ha2Input := req.Method + ":" + uri
	if qop == "auth-int" {
		bodyHash, err := hashRequestBody(hash, req)
		if err != nil {
			return "", err
		}
		ha2Input += ":" + bodyHash
	}
	ha2 := hashHex(hash, ha2Input)
	var response string
	if qop != "" {
		response = hashHex(hash, ha1+":"+nonce+":"+nc+":"+cnonce+":"+qop+":"+ha2)
	} else {
		response = hashHex(hash, ha1+":"+nonce+":"+ha2)
	}
	parts := []string{
		`username="` + escapeAuthValue(user) + `"`,
		`realm="` + escapeAuthValue(realm) + `"`,
		`nonce="` + escapeAuthValue(nonce) + `"`,
		`uri="` + escapeAuthValue(uri) + `"`,
		`response="` + response + `"`,
	}
	if params["opaque"] != "" {
		parts = append(parts, `opaque="`+escapeAuthValue(params["opaque"])+`"`)
	}
	parts = append(parts, "algorithm="+algorithm)
	if qop != "" {
		parts = append(parts, "qop="+qop, "nc="+nc, `cnonce="`+cnonce+`"`)
	}
	return "Digest " + strings.Join(parts, ", "), nil
}

func digestHash(algorithm string) func() hash.Hash {
	base := strings.TrimSuffix(algorithm, "-SESS")
	switch base {
	case "MD5":
		return func() hash.Hash { return md5.New() }
	case "SHA-256":
		return func() hash.Hash { return sha256.New() }
	case "SHA-512-256":
		return func() hash.Hash { return sha512.New512_256() }
	}
	return nil
}

func hashHex(hash func() hash.Hash, value string) string {
	h := hash()
	_, _ = h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}

func escapeAuthValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func hashRequestBody(hash func() hash.Hash, req *http.Request) (string, error) {
	if req.GetBody == nil {
		return hashHex(hash, ""), nil
	}
	body, err := req.GetBody()
	if err != nil {
		return "", err
	}
	defer body.Close()
	h := hash()
	if _, err := io.Copy(h, body); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

const maxNonceCounts = 4096

type nonceCountEntry struct {
	count uint32
}

var (
	nonceCountMu sync.Mutex
	nonceCounts  = make(map[string]*nonceCountEntry)
)

func nextNonceCount(u *url.URL, realm, nonce string) (string, error) {
	scheme, host, port := originOf(u)
	key := scheme + "://" + host + ":" + port + "\x00" + realm + "\x00" + nonce
	nonceCountMu.Lock()
	defer nonceCountMu.Unlock()
	entry := nonceCounts[key]
	if entry == nil {
		if len(nonceCounts) >= maxNonceCounts {
			return "", fmt.Errorf("digest nonce count table is full")
		}
		entry = &nonceCountEntry{}
		nonceCounts[key] = entry
	}
	if entry.count == ^uint32(0) {
		return "", fmt.Errorf("digest nonce count exhausted")
	}
	entry.count++
	return fmt.Sprintf("%08x", entry.count), nil
}
