<p align="center">
	<img src="logo.png" border=0 width=500 alt="uhaha">
</p>
<p align="center">
<a href="https://travis-ci.org/tidwall/uhaha"><img src="https://img.shields.io/travis/tidwall/uhaha.svg?style=flat-square" alt="Build Status"></a>
<a href="https://godoc.org/github.com/tidwall/uhaha"><img src="https://img.shields.io/badge/api-reference-blue.svg?style=flat-square" alt="GoDoc"></a>
</p>

<p align="center">High Availabilty Framework for Happy Data</p>

Uhaha is a framework for building highly available data applications in Go. 
This is bascially an upgrade to my [Finn](https://github.com/tidwall/finn)
project, which was good but Uhaha is gooder because Uhaha has more security
features (TLS and auth tokens), customizable services, deterministic time,
recalculable random numbers, simpler snapshots, a smaller network footprint,
and other stuff too.  

## Features

- Simple API for quickly creating a fault-tolerant cluster.
- Deterministic monotonic time that does not drift and stays in sync with the internet.
- APIs for building custom services such as HTTP and gRPC. 
  Supports the Redis protocol by default, so any Redis-compatible client
  library will work with Uhaha.
- Multiple examples to help jumpstart integration, including
  a [Key-value DB](https://github.com/tidwall/uhaha/tree/master/examples/kvdb), 
  a [Timeseries DB](https://github.com/tidwall/uhaha/tree/master/examples/timeseries), 
  and a [Ticket Service](https://github.com/tidwall/uhaha/tree/master/examples/ticket).

## Example

Below a simple example of a service for monotonically increasing tickets. 

```go
package main

import "github.com/tidwall/uhaha"

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
```

### Building

Using the source file from the examples directory, we'll build an application
named "ticket"

```
go build -o ticket examples/ticket/main.go
```

### Running

It's ideal to have three, five, or seven nodes in your cluster.

Let's create the first node.

```
./ticket -n 1 -a :11001
```

This will create a node named 1 and bind the address to :11001

Now let's create two more nodes and add them to the cluster.

```
./ticket -n 2 -a :11002 -j :11001
./ticket -n 3 -a :11003 -j :11001
```

Now we have a fault-tolerant three node cluster up and running.

### Using

You can use any Redis compatible client, such as the redis-cli, telnet, 
or netcat.

I'll use the redis-cli in the example below.

Connect to the leader. This will probably be the first node you created.

```
redis-cli -p 11001
```

Send the server a TICKET command and receive the first ticket.

```
> TICKET
"1"
```

From here on every TICKET command will guarentee to generate a value larger
than the previous TICKET command.

```
> TICKET
"2"
> TICKET
"3"
> TICKET
"4"
> TICKET
"5"
```


## Built-in Commands

There are a number built-in commands for managing and monitor the cluster.

```sh
MACHINE                                 # show information about the machine
RAFT LEADER                             # show the address of the current raft leader
RAFT INFO [pattern]                     # show information about the raft server and cluster
RAFT SERVER LIST                        # show all servers in cluster
RAFT SERVER ADD id address              # add a server to cluster
RAFT SERVER REMOVE id                   # remove a server from the cluster
RAFT SNAPSHOT NOW                       # make a snapshot of the data
RAFT SNAPSHOT LIST                      # show a list of all snapshots on server
RAFT SNAPSHOT FILE id                   # show the file path of a snapshot on server
RAFT SNAPSHOT READ id [RANGE start end] # download all or part of a snapshot
```

## Command-line options

```
my-uhaha-app version: 0.0.0

Usage: my-uhaha-app [-n id] [-a addr] [options]

Basic options:
  -v              : display version
  -h              : display help, this screen
  -a addr         : bind to address  (default: 127.0.0.1:11001)
  -n id           : node ID  (default: 1)
  -d dir          : data directory  (default: data)
  -j addr         : leader address of a cluster to join
  -l level        : log level  (default: notice) [debug,verb,notice,warn,silent]

Security options:
  --tls-cert path : path to TLS certificate
  --tls-key path  : path to TLS private key
  --tls-ca path   : path to CA certificate to be used as a trusted root when 
                    validating certificates.
  --auth auth     : cluster authorization, shared by all servers and clients

Advanced options:
  --nosync        : turn off syncing data to disk after every write. This leads
                    to faster write operations but opens up the chance for data
                    loss due to catastrophic events such as power failure.
  --openreads     : allow followers to process read commands, but with the 
                    possibility of returning stale data.
  --localtime     : have the raft machine time synchronized with the local
                    server rather than the public internet. This will run the 
                    risk of time shifts when the local server time is
                    drastically changed during live operation. 
  --restore path  : restore a raft machine from a snapshot file. This will
                    start a brand new single-node cluster using the snapshot as
                    initial data. The other nodes must be re-joined. This
                    operation is ignored when a data directory already exists.
                    Cannot be used with -j flag.
```

