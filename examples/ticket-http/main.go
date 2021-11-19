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

	// Add an HTTP service that will call the write command created above.
	mux := http.NewServeMux()
	conf.AddService(buildService(mux))

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

func sniff(r io.Reader) bool {
	s := bufio.NewScanner(r)
	for s.Scan() {
		line := s.Text()
		if line == "" {
			continue
		}

		return strings.Contains(line, "HTTP/1.")
	}

	return false
}

func buildService(mux *http.ServeMux) (func(io.Reader) bool, func(uhaha.Service, net.Listener)){
	return sniff, func(s uhaha.Service, ln net.Listener) {
		mux.HandleFunc("/ticket", func(w http.ResponseWriter, r *http.Request) {
			recv := s.Send([]string{"ticket"}, nil)
			value, dur, err := recv.Recv()
			s.Log().Printf("Request took %s", dur)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(fmt.Sprintf("Error occured: %s\n", err)))
				return
			}

			switch v := value.(type) {
			case []byte:
				w.Write(v)
				break
			case int64:
				w.Write([]byte(fmt.Sprintf("%d\n", v)))
				break
			default:
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(fmt.Sprintf("Unable to parse value: %v\n", value)))
				return
			}
		})
		s.Log().Fatal(http.Serve(ln, mux))
	}
}
