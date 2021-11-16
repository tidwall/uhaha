// Copyright 2020 Joshua J Baker. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/tidwall/uhaha"
)

type service func(s uhaha.Service, ln net.Listener)

type data struct {
	Ticket int64
}

func main() {
	// Set up a uhaha configuration
	var conf uhaha.Config

	// Give the application a name. All servers in the cluster should use the
	// same name.
	conf.Name = "ticket"

	// Set the initial data. This is state of the data when first server in the
	// cluster starts for the first time ever.
	conf.InitialData = new(data)

	// Since we are not holding onto much data we can used the built-in JSON
	// snapshot system. You just need to make sure all the important fields in
	// the data are exportable (capitalized) to JSON. In this case there is
	// only the one field "Ticket".
	conf.UseJSONSnapshots = true

	// Add a command that will change the value of a Ticket.
	conf.AddWriteCommand("ticket", cmdTICKET)

	// Add a HTTP service that will call the write command created above.
	mux := http.NewServeMux() // You can wire up handlers to the mux.
	svc, teardown := httpService(mux)
	defer teardown()
	conf.AddService(sniffHTTP, svc)

	// Finally, hand off all processing to uhaha.
	uhaha.Main(conf)
}

// TICKET
// help: returns a new ticket that has a value that is at least one greater
// than the previous TICKET call.
func cmdTICKET(m uhaha.Machine, args []string) (interface{}, error) {
	// The the current data from the machine
	data := m.Data().(*data)

	// Increment the ticket
	data.Ticket++

	// Return the new ticket to caller
	return data.Ticket, nil
}

// sniffHTTP inspects the first line of the io.Reader
// to determine if this is an HTTP call.
func sniffHTTP(r io.Reader) bool {
	s := bufio.NewScanner(r)
	tokens := strings.SplitN(s.Text(), " ", 2)
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

// httpService sets up a new HTTP server using the provided mux
// and uses the uhaha.Service to send commands.
// The handler simply calls the ticket command and waits for the response.
func httpService(mux *http.ServeMux) (service, func() error) {
	srv := http.Server{
		Handler: mux,
	}
	svc := func(s uhaha.Service, ln net.Listener) {
		mux.HandleFunc("/ticket", func(w http.ResponseWriter, req *http.Request) {
			ctx, accept := s.Opened(req.RemoteAddr)
			if !accept {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			defer s.Closed(ctx, req.RemoteAddr)
			r := s.Send([]string{"ticket"}, nil)
			v, _, err := r.Recv()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(err.Error()))
				return
			}
			w.Write([]byte(fmt.Sprintf("%v\n", v)))
		})
		s.Log().Printf("HTTP server listening on %s", ln.Addr().String())
		s.Log().Fatal(srv.Serve(ln))
	}

	return svc, srv.Close
}
