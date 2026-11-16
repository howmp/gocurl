package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"gocurl/internal/config"
	"gocurl/internal/daemon"
	"gocurl/internal/exitcode"
	"gocurl/internal/ftp"
	"gocurl/internal/httpx"
	"gocurl/internal/output"
	"gocurl/internal/tlsname"
	"gocurl/internal/websocket"
	"io"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

var version = "0.1.0"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "--daemon":
			return daemon.Run(args[1:])
		case "--stop-daemon":
			return stopDaemonCommand(args[1:])
		}
	}
	return run(args, stdout, stderr)
}

func stopDaemonCommand(args []string) int {
	fs := flag.NewFlagSet("gocurl-stop-daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", daemon.SocketPath(), "unix socket path")
	session := fs.String("session", "", "session name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *session != "" {
		*socket = daemon.SocketPathFor(*session)
	}
	if err := daemon.Stop(*socket); err != nil {
		fmt.Fprintf(os.Stderr, "gocurl: --stop-daemon: %s\n", redactErrorText(err.Error()))
		return 1
	}
	return 0
}

func run(args []string, stdout, stderr io.Writer) int {
	expanded, err := config.ExpandConfigArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gocurl: %s\n", err)
		return 2
	}
	ops, err := parseOperations(expanded)
	if err != nil {
		fmt.Fprintf(stderr, "gocurl: %s\n", err)
		fmt.Fprintf(stderr, "gocurl: try 'gocurl --help' for more information\n")
		return 2
	}
	for _, o := range ops {
		if o.Help {
			printHelp(stdout)
			return 0
		}
	}
	for _, o := range ops {
		if o.Version {
			printVersion(stdout)
			return 0
		}
	}
	exitCode := 0
	for _, o := range ops {
		code := runOptions(o, stdout, stderr)
		if exitCode == 0 {
			exitCode = code
		}
	}
	return exitCode
}

func runOptions(o *config.Options, stdout, stderr io.Writer) int {
	o.NormalizeCookie()
	o.TLSState = config.NewTLSStateStore()
	if o.JsonOutput && o.WriteOut == "" && !o.JsonEnvelope {
		o.WriteOut = "%{json}"
	}
	if o.JsonEnvelope && o.DumpHeader == "-" {
		fmt.Fprintln(stderr, "gocurl: --json-envelope cannot be combined with --dump-header -")
		return 2
	}
	if len(o.Urls) == 0 {
		fmt.Fprintln(stderr, "gocurl: no URL specified")
		fmt.Fprintln(stderr, "gocurl: try 'gocurl --help' for more information")
		return 2
	}
	if o.RetryMaxPerHost > 0 {
		o.RetryBudget = config.NewRetryBudget(o.RetryMaxPerHost)
	}
	if o.Parallel && len(o.Urls) > 1 && len(o.Outputs) > 0 && len(o.Outputs) < len(o.Urls) {
		if !o.Silent || o.ShowError {
			fmt.Fprintln(stderr, "gocurl: (2) option -Z/--parallel: each URL requires its own -o output")
		}
		return 2
	}

	if o.DumpHeader != "" {
		output.ResetHeaderDump()
	}
	if o.Persist && hasOnlyHTTPURLs(o.Urls) && !o.RawHTTP && o.HttpVersion != "2-prior" && o.LimitRate == 0 && !isStreamingUpload(o) && !config.CanStreamForm(o) && !cookieFileRoundTrip(o) {
		if socket, daemonErr := daemon.Ensure(o); daemonErr == nil {
			o.DaemonSocket = socket
		} else if o.Verbose {
			fmt.Fprintf(stderr, "gocurl: daemon unavailable, using direct connections: %s\n", daemonErr)
		}
	}

	o.CookieJar = httpx.NewCookieJar()
	if o.CookieJarFile != "" {
		o.CookieHosts = config.NewCookieHostSet()
	}
	if o.CookieFile != "" {
		if err := httpx.LoadCookieFile(o.CookieJar, o.CookieFile); err != nil {
			reportError(stderr, o, 26, err)
			return 26
		}
	}

	var exitCode int
	if o.Parallel && len(o.Urls) > 1 {
		exitCode = runParallel(o, stdout, stderr)
	} else {
		for i, rawURL := range o.Urls {
			if err := runOne(o, rawURL, i, stdout, stderr); err != nil {
				code := exitcode.From(err)
				reportError(stderr, o, code, err)
				if exitCode == 0 {
					exitCode = code
				}
			}
		}
	}

	if o.CookieJarFile != "" {
		if o.DaemonSocket != "" {
			hosts := append(append([]string(nil), o.Urls...), o.CookieHosts.List()...)
			text, err := daemon.DumpCookies(o.DaemonSocket, hosts)
			if err == nil {
				if o.CookieJarFile == "-" {
					_, err = io.WriteString(stdout, text)
				} else {
					err = output.WriteFile(daemon.AbsPath(o.CookieJarFile), []byte(text))
					if err != nil {
						err = &exitcode.WriteError{Err: err}
					}
				}
			}
			if err != nil {
				reportError(stderr, o, 23, err)
				if exitCode == 0 {
					exitCode = 23
				}
			}
		} else if err := httpx.SaveCookieFile(o.CookieJar, o.CookieJarFile, stdout); err != nil {
			reportError(stderr, o, 23, err)
			if exitCode == 0 {
				exitCode = 23
			}
		}
	}
	return exitCode
}

func parseOperations(args []string) ([]*config.Options, error) {
	segments := splitNextArgs(args)
	if len(segments) == 1 && len(segments[0]) == 0 {
		return []*config.Options{config.NewOptions()}, nil
	}
	ops := make([]*config.Options, 0, len(segments))
	for i, segment := range segments {
		if len(segment) == 0 {
			return nil, fmt.Errorf("option --next: operation %d is empty", i+1)
		}
		o, err := config.ParseArgs(segment)
		if err != nil {
			return nil, err
		}
		if len(o.Urls) == 0 && !o.Help && !o.Version && len(segments) > 1 {
			return nil, fmt.Errorf("option --next: operation %d has no URL", i+1)
		}
		ops = append(ops, o)
	}
	return ops, nil
}

func splitNextArgs(args []string) [][]string {
	segments := [][]string{{}}
	afterDashes := false
	for _, arg := range args {
		if !afterDashes && arg == "--next" {
			segments = append(segments, nil)
			continue
		}
		if !afterDashes && arg == "--" {
			afterDashes = true
		}
		segments[len(segments)-1] = append(segments[len(segments)-1], arg)
	}
	return segments
}

func runParallel(o *config.Options, stdout, stderr io.Writer) int {
	type result struct {
		out *os.File
		err bytes.Buffer
	}
	results := make([]result, len(o.Urls))
	cleanup := func(i int) {
		if results[i].out == nil {
			return
		}
		name := results[i].out.Name()
		_ = results[i].out.Close()
		_ = os.Remove(name)
		results[i].out = nil
	}
	defer func() {
		for i := range results {
			cleanup(i)
		}
	}()
	errs := make([]error, len(o.Urls))
	parallelLimit := o.ParallelMax
	if o.DaemonSocket != "" && parallelLimit > daemon.MaxConnections {
		parallelLimit = daemon.MaxConnections
	}
	sem := make(chan struct{}, parallelLimit)
	var wg sync.WaitGroup
	for i := range o.Urls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			file, err := os.CreateTemp("", "gocurl-parallel-*")
			if err != nil {
				errs[i] = fmt.Errorf("cannot create parallel output: %w", err)
				return
			}
			_ = file.Chmod(0o600)
			_ = os.Remove(file.Name())
			results[i].out = file
			errs[i] = runOne(o, o.Urls[i], i, results[i].out, &results[i].err)
		}(i)
	}
	wg.Wait()

	exitCode := 0
	for i := range results {
		if results[i].out == nil {
			if errs[i] != nil {
				code := exitcode.From(errs[i])
				reportError(stderr, o, code, errs[i])
				if exitCode == 0 {
					exitCode = code
				}
			}
			continue
		}
		if _, err := results[i].out.Seek(0, io.SeekStart); err != nil {
			reportError(stderr, o, exitcode.WriteFailure, err)
			if exitCode == 0 {
				exitCode = exitcode.WriteFailure
			}
		}
		if _, err := io.Copy(stdout, results[i].out); err != nil {
			reportError(stderr, o, exitcode.WriteFailure, err)
			if exitCode == 0 {
				exitCode = exitcode.WriteFailure
			}
		}
		if _, err := io.Copy(stderr, &results[i].err); err != nil && exitCode == 0 {
			exitCode = exitcode.WriteFailure
		}
		cleanup(i)
		if errs[i] != nil {
			code := exitcode.From(errs[i])
			reportError(stderr, o, code, errs[i])
			if exitCode == 0 {
				exitCode = code
			}
		}
	}
	return exitCode
}

type httpTransferPlan struct {
	rangeOffset int64
	formStream  *config.MultipartStream
	upload      *uploadStream
	body        []byte
	contentType string
	bodyLen     int64
	method      string
	ctx         context.Context
	cancel      context.CancelFunc
}

func (p *httpTransferPlan) buildRequest(o *config.Options, u *url.URL, authHeader string, env *jsonEnvelope) (*http.Request, error) {
	var bodyReader io.Reader
	if p.formStream != nil {
		reader, err := p.formStream.Open()
		if err != nil {
			return nil, err
		}
		bodyReader = reader
	} else if p.upload != nil {
		f, err := p.upload.open()
		if err != nil {
			return nil, err
		}
		bodyReader = f
	} else if len(p.body) > 0 {
		bodyReader = bytes.NewReader(p.body)
	}
	if bodyReader != nil && o.LimitRate > 0 {
		bodyReader = output.NewRateReader(p.ctx, bodyReader, o.LimitRate)
	}
	req, err := http.NewRequestWithContext(p.ctx, p.method, u.String(), bodyReader)
	if err != nil {
		if closer, ok := bodyReader.(io.Closer); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	if p.formStream != nil {
		req.GetBody = p.formStream.Open
	} else if p.upload != nil {
		req.GetBody = p.upload.open
	} else if len(p.body) > 0 {
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(p.body)), nil
		}
	}
	httpx.ApplyHeadersWithLength(req, o, p.contentType, p.body, p.bodyLen)
	if p.upload != nil && p.upload.offset > 0 {
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", p.upload.offset, p.upload.size-1, p.upload.size))
		order := req.Header[http.HeaderOrderKey]
		req.Header[http.HeaderOrderKey] = httpx.HeaderOrderInsertBefore(order, "content-range", "accept-encoding")
	} else if p.rangeOffset > 0 && o.RangeSpec == "" {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", p.rangeOffset))
		order := req.Header[http.HeaderOrderKey]
		req.Header[http.HeaderOrderKey] = httpx.HeaderOrderInsertBefore(order, "range", "accept-encoding")
	}
	if o.CookieHeader != "" && req.Header.Get("Cookie") == "" && !headerRemoved(o, "Cookie") {
		req.Header.Set("Cookie", o.CookieHeader)
	}
	if authHeader != "" && req.Header.Get("Authorization") == "" && !headerRemoved(o, "Authorization") {
		req.Header.Set("Authorization", authHeader)
		order := req.Header[http.HeaderOrderKey]
		req.Header[http.HeaderOrderKey] = httpx.HeaderOrderInsertBefore(order, "authorization", "accept-encoding")
	}
	if o.DaemonSocket == "" && o.RequestTarget != "" {
		req.URL.Opaque = o.RequestTarget
	}
	env.setRequest(req, o, p.bodyLen)
	return req, nil
}

func prepareHTTPTransfer(o *config.Options, u *url.URL, idx int, env *jsonEnvelope) (*httpTransferPlan, error) {
	var err error
	rangeOffset := int64(0)
	if o.ContinueAtSet {
		if isStreamingUpload(o) && o.ResumeOffset < 0 {
			return nil, fmt.Errorf("option --continue-at: '-' is not supported for uploads")
		}
		offset, err := output.ResolveResumeOffset(o, u, idx)
		if err != nil {
			return nil, err
		}
		rangeOffset = offset
	}

	var upload *uploadStream
	var formStream *config.MultipartStream
	var body []byte
	var contentType string
	if len(o.Forms) > 0 && o.DaemonSocket == "" && config.CanStreamForm(o) {
		formStream, err = config.NewMultipartStream(o.Forms)
		if err != nil {
			return nil, err
		}
		contentType = formStream.ContentType()
	} else if isStreamingUpload(o) {
		upload, err = newUploadStream(o.UploadFile, rangeOffset)
		if err != nil {
			return nil, err
		}
		contentType = "application/octet-stream"
	} else {
		body, contentType, err = httpx.BuildBody(o)
		if err != nil {
			return nil, err
		}
	}
	hasData := len(o.Data) > 0 || o.JsonSet || o.UploadFile != "" || len(o.Forms) > 0
	if o.Get && len(body) > 0 {
		if err := validateQueryBytes(string(body)); err != nil {
			return nil, err
		}
		if u.RawQuery != "" {
			u.RawQuery += "&"
		}
		u.RawQuery += string(body)
		body = nil
		contentType = ""
	}
	bodyLen := int64(len(body))
	if upload != nil {
		bodyLen = upload.size - upload.offset
	}
	if formStream != nil {
		bodyLen = formStream.Length()
	}

	method := httpx.RequestMethod(o, hasData)
	if err := httpx.ValidateUserHeaders(o); err != nil {
		return nil, err
	}
	if o.RequestTarget != "" {
		if err := httpx.ValidateRequestTarget(o.RequestTarget); err != nil {
			return nil, err
		}
	}
	ctx := context.Background()
	var cancel context.CancelFunc
	if o.MaxTime > 0 {
		ctx, cancel = context.WithTimeout(ctx, o.MaxTime)
	}

	plan := &httpTransferPlan{
		rangeOffset: rangeOffset,
		formStream:  formStream,
		upload:      upload,
		body:        body,
		contentType: contentType,
		bodyLen:     bodyLen,
		method:      method,
		ctx:         ctx,
		cancel:      cancel,
	}
	return plan, nil
}

func runOne(o *config.Options, rawURL string, idx int, stdout, stderr io.Writer) (retErr error) {
	u, err := httpx.NormalizeURL(rawURL)
	if err != nil {
		return err
	}
	var env *jsonEnvelope
	if o.JsonEnvelope {
		env = newJSONEnvelope(u)
		defer func() {
			env.finish(retErr)
			if err := env.write(stdout); err != nil && retErr == nil {
				retErr = err
			}
		}()
	}
	if o.JsonEnvelope && (u.Scheme == "ftp" || u.Scheme == "ftps" || u.Scheme == "ws" || u.Scheme == "wss") {
		return fmt.Errorf("option --json-envelope is only supported for HTTP/HTTPS")
	}
	if o.JsonEnvelope && o.RawHTTP {
		return fmt.Errorf("option --json-envelope is not supported in raw HTTP mode")
	}
	switch u.Scheme {
	case "ftp", "ftps":
		if err := validateHTTPOnlyOptions(o); err != nil {
			return err
		}
		if o.MaxFileSize > 0 {
			return fmt.Errorf("option --max-filesize is not supported for FTP/FTPS")
		}
		return ftp.Run(o, u, idx, stdout, stderr)
	case "ws", "wss":
		if o.ContinueAtSet {
			return fmt.Errorf("option --continue-at is not supported for WS/WSS")
		}
		if err := validateHTTPOnlyOptions(o); err != nil {
			return err
		}
		return websocket.Run(o, u, idx, stdout, stderr)
	}
	if o.RawHTTP {
		return runRawHTTP(o, u, idx, stdout, stderr)
	}
	plan, err := prepareHTTPTransfer(o, u, idx, env)
	if err != nil {
		return err
	}
	if plan.cancel != nil {
		defer plan.cancel()
	}
	ctx := plan.ctx

	start := time.Now()
	execution, err := executeHTTPTransfer(o, u, plan, env, stderr, start)
	if err != nil {
		return err
	}
	if execution.cleanup != nil {
		defer execution.cleanup()
	}
	defer execution.resp.Body.Close()

	return finalizeHTTPResponse(o, u, idx, stdout, stderr, start, ctx, execution.resp, execution.info, env, plan.rangeOffset)
}

type httpExecution struct {
	resp    *http.Response
	info    *output.ResponseInfo
	cleanup func()
}

type transferAttempt struct {
	o       *config.Options
	u       *url.URL
	ctx     context.Context
	method  string
	start   time.Time
	stderr  io.Writer
	attempt int
	info    *output.ResponseInfo
}

func (a *transferAttempt) retry(retryable bool, statusResp *http.Response) (bool, error) {
	if !retryable || a.attempt >= a.o.Retries {
		return false, nil
	}
	retried, err := retryHTTP(a.ctx, a.o, a.u, a.method, a.attempt, a.start, a.info, retryDelayFor(a.o, a.attempt, statusResp), a.stderr)
	if err != nil {
		return false, err
	}
	if retried {
		a.attempt = a.info.NumRetries
	}
	return retried, nil
}

func (a *transferAttempt) retryOrContinue(retryable bool, statusResp *http.Response, onRetry func()) (bool, error) {
	retried, err := a.retry(retryable, statusResp)
	if err != nil {
		return false, err
	}
	if retried && onRetry != nil {
		onRetry()
	}
	return retried, nil
}

func executeHTTPTransfer(o *config.Options, u *url.URL, plan *httpTransferPlan, env *jsonEnvelope, stderr io.Writer, start time.Time) (*httpExecution, error) {
	var err error
	body := plan.body
	method := plan.method
	ctx := plan.ctx

	state := &transferAttempt{o: o, u: u, ctx: ctx, method: method, start: start, stderr: stderr}
	authHeader := ""
	authStage := 0
	authTries := 0
	if o.AuthMode == "ntlm" || o.AuthMode == "negotiate" {
		if user, pass, ok := httpx.CredentialsForURL(o, u); ok {
			authHeader, authStage = httpx.InitialNTLMHeader(o, user, pass)
		}
	}
	var (
		resp    *http.Response
		cleanup func()
		info    *output.ResponseInfo
	)
	useDaemon := o.DaemonSocket != ""
	var directDoer httpx.Doer
	if !useDaemon {
		directDoer, err = httpx.NewHTTPDoer(o, u)
		if err != nil {
			return nil, err
		}
		defer directDoer.CloseIdleConnections()
	}
	for {
		req, err := plan.buildRequest(o, u, authHeader, env)
		if err != nil {
			return nil, err
		}

		info = &output.ResponseInfo{Url: u.String(), Scheme: u.Scheme, Started: start, NumRetries: state.attempt}
		state.info = info
		if o.Verbose {
			output.PrintRequestVerbose(stderr, req, o)
		}

		if useDaemon {
			var remoteAddr string
			resp, remoteAddr, cleanup, err = daemon.Do(o, req, body)
			if errors.Is(err, daemon.ErrRequestBodyTooLarge) || errors.Is(err, daemon.ErrBusy) {
				useDaemon = false
				if cleanup != nil {
					cleanup()
					cleanup = nil
				}
				directDoer, err = httpx.NewHTTPDoer(o, u)
				if err != nil {
					return nil, err
				}
				defer directDoer.CloseIdleConnections()
				continue
			} else if err != nil {
				if cleanup != nil {
					cleanup()
					cleanup = nil
				}
				if retried, waitErr := state.retryOrContinue(ctx.Err() == nil && shouldRetryError(err, o), nil, nil); waitErr != nil {
					return nil, waitErr
				} else if retried {
					continue
				}
				return nil, err
			} else {
				info.RemoteAddr = remoteAddr
			}
		}
		if !useDaemon {
			req = httpx.AddTrace(req, o, stderr, info)
			resp, err = directDoer.Do(req)
			if err != nil {
				if resp != nil {
					_ = resp.Body.Close()
					resp = nil
				}
				if retried, waitErr := state.retryOrContinue(ctx.Err() == nil && shouldRetryError(err, o), nil, nil); waitErr != nil {
					return nil, waitErr
				} else if retried {
					continue
				}
				return nil, err
			}
		}
		if resp.StatusCode == 401 && authTries < 3 && !hasHeaderOption(o, "Authorization") {
			authURL := u
			if resp.Request != nil && resp.Request.URL != nil {
				authURL = resp.Request.URL
			}
			if !httpx.SameOriginURL(authURL, u) {
				authURL = nil
			}
			if authURL != nil {
				user, pass, ok := httpx.CredentialsForURL(o, authURL)
				if ok {
					next, nextStage, ok, authErr := httpx.NextAuthHeader(o, req, resp, body, authStage, user, pass)
					if authErr != nil {
						return nil, authErr
					}
					if ok {
						if !httpx.SameOriginURL(authURL, u) {
							return nil, fmt.Errorf("refusing to send credentials across origins")
						}
						authHeader = next
						authStage = nextStage
						authTries++
						_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
						_ = resp.Body.Close()
						if cleanup != nil {
							cleanup()
							cleanup = nil
						}
						continue
					}
				}
			}
		}
		if retried, waitErr := state.retryOrContinue(ctx.Err() == nil && shouldRetryStatus(resp.StatusCode), resp, func() {
			_ = resp.Body.Close()
			if cleanup != nil {
				cleanup()
				cleanup = nil
			}
		}); waitErr != nil {
			return nil, waitErr
		} else if retried {
			continue
		}
		break
	}
	return &httpExecution{resp: resp, info: info, cleanup: cleanup}, nil
}

func finalizeHTTPResponse(o *config.Options, u *url.URL, idx int, stdout, stderr io.Writer, start time.Time, ctx context.Context, resp *http.Response, info *output.ResponseInfo, env *jsonEnvelope, rangeOffset int64) error {
	httpx.ApplyTLSState(o, resp)
	info.StatusCode = resp.StatusCode
	info.HttpVersion = shortHTTPVersion(resp.Proto)
	info.ContentType = resp.Header.Get("Content-Type")
	info.Url = resp.Request.URL.String()
	recordCookieHosts(o, resp.Request)
	info.SizeHeader = output.ResponseHeaderSize(resp)
	info.NumRedirects = redirectCount(resp.Request)
	info.SslVerifyResult = sslVerifyResult(resp, o)
	if resp.TLS != nil {
		info.TlsVersion = tlsname.Version(resp.TLS.Version)
		info.Alpn = resp.TLS.NegotiatedProtocol
	}
	if info.RemoteAddr == "" {
		info.RemoteAddr = resp.Request.URL.Hostname()
	}
	if o.Verbose {
		output.PrintResponseVerbose(stderr, resp, o.ShowSecrets)
	}
	if err := output.WriteHeaderDump(o, resp, idx, stdout); err != nil {
		return err
	}
	outOptions, err := resumeOutputOptions(o, rangeOffset, u, idx, resp)
	if err != nil {
		return err
	}
	if o.JsonEnvelope {
		return finishEnvelopeResponse(ctx, outOptions, env, resp, info, u, idx, start, stdout)
	}
	return emitResponse(o, outOptions, u, idx, stdout, resp, info, ctx, start)
}

func headerRemoved(o *config.Options, name string) bool {
	for _, h := range o.Headers {
		if strings.EqualFold(h.Name, name) && h.Remove {
			return true
		}
	}
	return false
}

func recordCookieHosts(o *config.Options, req *http.Request) {
	if o == nil || o.CookieHosts == nil {
		return
	}
	for count := 0; req != nil && count < 100; count++ {
		if req.URL != nil {
			o.CookieHosts.Add(req.URL.String())
		}
		if req.Response == nil {
			break
		}
		req = req.Response.Request
	}
}

func cookieFileRoundTrip(o *config.Options) bool {
	return o.CookieJarFile != "" && o.CookieFile != "" && o.CookieFile != "-"
}

func hasHeaderOption(o *config.Options, name string) bool {
	for _, h := range o.Headers {
		if strings.EqualFold(h.Name, name) {
			return true
		}
	}
	return false
}

func validateHTTPOnlyOptions(o *config.Options) error {
	checks := []struct {
		used   bool
		option string
	}{
		{len(o.Forms) > 0, "--form"},
		{o.RangeSpec != "", "--range"},
		{o.Retries > 0, "--retry"},
		{o.FailWithBody, "--fail-with-body"},
		{o.UnixSocket != "", "--unix-socket"},
		{o.LimitRate > 0, "--limit-rate"},
	}
	for _, check := range checks {
		if check.used {
			return fmt.Errorf("option %s is only supported for HTTP/HTTPS", check.option)
		}
	}
	return nil
}

func redirectCount(req *http.Request) int {
	count := 0
	for req != nil && req.Response != nil && count < 100 {
		count++
		req = req.Response.Request
	}
	return count
}

func sslVerifyResult(resp *http.Response, o *config.Options) int {
	if resp.TLS == nil {
		return 0
	}
	if o.Insecure {
		return 18
	}
	if len(resp.TLS.VerifiedChains) > 0 {
		return 0
	}
	return 0
}

func isTimeoutError(err error) bool {
	return exitcode.IsTimeout(err)
}

func newTimeoutError(o *config.Options, bytesReceived int64) error {
	if o.MaxTime > 0 {
		return &exitcode.Coded{Code: exitcode.Timeout, Msg: fmt.Sprintf(
			"Operation timed out after %d milliseconds with %d bytes received",
			o.MaxTime.Milliseconds(), bytesReceived)}
	}
	return context.DeadlineExceeded
}

func validateQueryBytes(query string) error {
	for i := 0; i < len(query); i++ {
		if query[i] <= ' ' || query[i] == 0x7f {
			return &exitcode.Coded{Code: exitcode.BadURL, Msg: "option --get: request query contains whitespace or control characters"}
		}
	}
	return nil
}

func hasOnlyHTTPURLs(urls []string) bool {
	for _, raw := range urls {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		u, err := httpx.NormalizeURL(raw)
		if err != nil {
			return false
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return false
		}
	}
	return true
}

func shortHTTPVersion(proto string) string {
	switch {
	case strings.HasPrefix(proto, "HTTP/2"):
		return "2"
	case strings.HasPrefix(proto, "HTTP/1.1"):
		return "1.1"
	case strings.HasPrefix(proto, "HTTP/1.0"):
		return "1.0"
	default:
		return proto
	}
}

var credentialInURL = regexp.MustCompile(`://[^/\s@]+@`)

func redactErrorText(text string) string {
	return credentialInURL.ReplaceAllString(text, "://[REDACTED]@")
}

func reportError(stderr io.Writer, o *config.Options, code int, err error) {
	if !o.Silent || o.ShowError {
		fmt.Fprintf(stderr, "gocurl: (%d) %s\n", code, redactErrorText(err.Error()))
	}
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "gocurl %s (%s/%s) utls/%s\n", version, runtime.GOOS, runtime.GOARCH, moduleVersion("github.com/bogdanfinn/utls"))
	fmt.Fprintln(w, "Protocols: http https ftp ftps ws wss")
	fmt.Fprintln(w, "Features: HTTP HTTP2 HTTPS ECH FTP FTPS WS WSS H2C")
}

func moduleVersion(path string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path == path {
			return dep.Version
		}
	}
	return "unknown"
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `Usage: gocurl [options] <url>

Only HTTP, HTTPS, FTP, FTPS, WS and WSS are supported. Other protocols are
reported as "Protocol ... not supported". Options use curl-compatible names.

  -X, --request <method>        HTTP method
      --url <url>               URL to request (repeatable)
  -H, --header <header>         Add or override a header (repeatable)
  -d, --data <data>             Request body, implies POST
      --data-raw <data>         Body without @file interpretation
      --data-binary <data>      Body preserving newlines
      --data-urlencode <data>   URL-encoded body part
      --json <data>             JSON body, implies POST
  -F, --form <name=value>       Multipart form field (repeatable)
      --form-string <name=value> Literal form field value
  -G, --get                     Put body data in the URL query
  -T, --upload-file <file>      Upload file (FTP/FTPS STOR, HTTP PUT)
  -l, --list-only               FTP/FTPS names-only listing
  -P, --ftp-port <address>      Active FTP data connection listen address
      --ftp-pasv                Force passive FTP mode (default)
      --ftp-ssl                 Explicit FTP TLS via AUTH TLS (also --ssl)
      --ssl-reqd                Require FTP TLS
      --dns-servers <servers>   Custom DNS resolvers, comma-separated
      --doh-url <url>           DNS-over-HTTPS resolver URL
      --doh-insecure            Skip DoH TLS certificate verification
  -A, --user-agent <string>     User-Agent header
  -e, --referer <url>           Referer header
  -b, --cookie <string|file>    Cookie string or Netscape cookie file
  -c, --cookie-jar <file>       Write cookies in Netscape format
  -u, --user <user:pass>        Basic authentication
      --basic                   Basic authentication
      --digest                  Digest authentication
      --ntlm                    NTLM authentication
      --negotiate               Negotiate (NTLM token fallback)
      --anyauth                 Pick Basic/Digest/NTLM from challenge
      --netrc                    Read credentials from ~/.netrc
      --netrc-file <file>        Read credentials from a netrc file
  -x, --proxy <url>             HTTP/HTTPS/SOCKS proxy
  -U, --proxy-user <user:pass>  Proxy credentials
      --proxy-header <header>   Extra header for proxy CONNECT
      --proxy-insecure          Skip HTTPS proxy certificate verification
      --preproxy <url>          Proxy used to reach the final proxy
      --noproxy <hosts>         Comma-separated proxy bypass list
      --connect-to <h:p:h:p>    Map host:port to another endpoint
      --unix-socket <path>      Connect HTTP over a Unix socket
      --abstract-unix-socket <name>  Connect over an abstract Unix socket
      --interface <name|ip>     Bind the source interface/address
  -E, --cert <file>             Client certificate (PEM/P12/DER)
      --key <file>              Client private key
      --cert-type <type>        PEM, P12, DER
      --key-type <type>         PEM, DER
      --pass <phrase>           Key/P12 password
      --cacert <file>           Custom CA bundle
      --capath <dir>            Directory of CA certificates
  -K, --config <file>           Read a curl-style config file
  -o, --output <file>           Write body to file (repeatable)
  -O, --remote-name             Use remote file name
      --create-dirs             Create output directories
  -i, --include                 Include response headers
  -I, --head                    Send HEAD and show response headers
  -D, --dump-header <file>      Write response headers to file
  -L, --location                Follow redirects
      --location-trusted        Forward credentials across redirects
      --post301                  Keep POST method after 301
      --post302                  Keep POST method after 302
      --post303                  Keep POST method after 303
      --max-redirs <num>        Maximum redirects (default 50)
      --resolve <host:port:addr[,addr]>  Resolve host:port to address
      --header-order <names>    Custom request-header order
      --fail-with-body          Keep body on HTTP >= 400, return 22
      --retry <num>             Retry transient failures
      --retry-delay <seconds>   Delay between retries
      --retry-max-time <sec>    Maximum total retry time
      --retry-all-errors        Retry all errors
      --retry-connrefused       Retry connection refused
      --retry-idempotent        Only retry idempotent methods
      --retry-max-per-host <n>  Maximum retries per host (0 = unlimited)
  -r, --range <range>           HTTP Range request
      --continue-at <offset|->  Resume a download/upload (HTTP/FTP)
      --limit-rate <rate>       Limit upload/download rate
      --max-filesize <size>     Reject larger responses
      --next                    Start a new operation with its own options
  -k, --insecure                Skip TLS certificate verification
  -f, --fail                    Fail on HTTP status >= 400
      --compressed              Request compressed response
      --raw                     Do not decode content encoding
      --tr-encoding             Request compressed transfer encoding
      --http1.0                 Raw HTTP/1.0 request mode
      --http1.1                 Use HTTP/1.1
      --http2                   Prefer HTTP/2, fall back to HTTP/1.1
      --http2-prior-knowledge   Use cleartext h2c
      --raw-http                Explicit raw HTTP/1.x mode
      --raw-request <file>      Send exact request bytes from a file
      --request-target <path>   Custom HTTP request target
  -Z, --parallel                Run transfers in parallel
      --parallel-max <num>      Maximum parallel transfers (default 50)
      --session <name>          Isolate daemon cookies/connections
      --persist                 Reuse background daemon/connections
      --no-persist              Disable background daemon/connection reuse
      --max-time <seconds>      Overall timeout
      --connect-timeout <sec>   Connection timeout
  -w, --write-out <format>      Print variables after the transfer (supports %{json})
      --json-output             Print %{json} after the transfer
      --json-envelope           Emit one structured JSON object per request
      --show-secrets            Do not redact credentials in verbose/envelope output
  -4, --ipv4                    Resolve to IPv4
  -6, --ipv6                    Resolve to IPv6
  -s, --silent                  Silent mode
  -S, --show-error              Show errors with --silent
  -N, --no-buffer               Disable output buffering
  -v, --verbose                 Verbose request/response output
  -h, --help                    Show this help
  -V, --version                 Show version
`)
}

func resumeOutputOptions(o *config.Options, offset int64, u *url.URL, idx int, resp *http.Response) (*config.Options, error) {
	if o == nil || !o.ContinueAtSet || offset <= 0 || o.UploadFile != "" {
		return o, nil
	}
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return nil, &exitcode.Coded{Code: exitcode.RangeError, Msg: "HTTP server does not support byte ranges: 416"}
	}
	if resp.StatusCode != http.StatusPartialContent {
		return nil, &exitcode.Coded{Code: exitcode.RangeError, Msg: fmt.Sprintf("HTTP server ignored resume request (status %d)", resp.StatusCode)}
	}
	if !contentRangeMatches(resp.Header.Get("Content-Range"), offset) {
		return nil, &exitcode.Coded{Code: exitcode.RangeError, Msg: "HTTP Content-Range does not match resume offset"}
	}
	return output.ResumeOutput(o, u, idx, offset, false)
}

func contentRangeMatches(value string, offset int64) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(strings.ToLower(value), "bytes ") {
		return false
	}
	value = strings.TrimSpace(value[len("bytes "):])
	start, _, ok := strings.Cut(value, "-")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(start), 10, 64)
	return err == nil && n == offset
}
