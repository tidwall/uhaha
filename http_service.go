package uhaha

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

type RequestHandler func(Service, *http.Request) Receiver
type ResponseHandler func(interface{}, error, http.ResponseWriter)

type contextKey string
const contextServiceKey contextKey = "service"

func serviceFromRequest(r *http.Request) (Service, bool) {
	v := r.Context().Value(contextServiceKey)
	if v == nil {
		return nil, false
	}
	s, ok := v.(Service)
	return s, ok
}

func isMovedError(err error) (string, bool) {
	tokens := strings.SplitN(err.Error(), " ", 2)
	if len(tokens) != 2 {
		return "", false
	}

	switch strings.ToUpper(tokens[0]) {
	case "TRY":
		if tokens[1] != "" {
			return tokens[1], true
		}
	case "MOVED":
		tokens = strings.SplitN(tokens[1], " ", 2)
		if len(tokens) != 2 {
			return "", false
		}
		if tokens[0] != "0" {
			return "", false
		}

		if tokens[1] != "" {
			return tokens[1], true
		}
	}
	return "", false
}

// sniffHTTP inspects the first line of the io.Reader
// to determine if this is an HTTP call.
func sniffHTTP(r io.Reader) bool {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := s.Text()
		if line == "" {
			continue
		}
		tokens := strings.SplitN(line, " ", 2)
		if len(tokens) != 2 {
			return false
		}

		switch strings.ToUpper(strings.TrimSpace(tokens[0])) {
		case http.MethodGet:
			return true
		case http.MethodHead:
			return true
		case http.MethodPost:
			return true
		case http.MethodPut:
			return true
		case http.MethodPatch:
			return true
		case http.MethodDelete:
			return true
		case http.MethodConnect:
			return true
		case http.MethodOptions:
			return true
		case http.MethodTrace:
			return true
		}

		return false
	}

	return false

}

func baseHandler(s Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sCtx, accept := s.Opened(req.RemoteAddr)
		if !accept {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer s.Closed(sCtx, req.RemoteAddr)
		ctx := context.WithValue(req.Context(), contextServiceKey, s)
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}

func HandleFunc(req RequestHandler, resp ResponseHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, ok := serviceFromRequest(r)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("Service not found in context"))
			return
		}
		recv := req(s, r)
		out, _, err := recv.Recv()
		if err != nil {
			addr, ok := isMovedError(err)
			if ok {
				scheme := r.URL.Scheme
				if scheme == "" {
					// TODO: Handle HTTPS here.
					scheme = "http"
				}
				newURL := fmt.Sprintf("%s://%s%s", scheme, addr, r.RequestURI)
				s.Log().Printf("redirecting command to %s", newURL)
				http.Redirect(w, r, newURL, http.StatusMovedPermanently)
				return
			}
		}
		resp(out, err, w)
	}
}

func HTTPService(handler http.Handler) (func(io.Reader) bool, func(Service, net.Listener)) {
	return sniffHTTP, func(s Service, ln net.Listener) {
		srv := &http.Server{
			Handler: baseHandler(s, handler),
		}
		defer srv.Close()
		s.Log().Printf("HTTP server listening on %s", ln.Addr().String())
		s.Log().Fatal(srv.Serve(ln))
	}
}
