# ticket-http

A variation of the [ticket](https://github.com/tidwall/uhaha/tree/master/examples/ticket) example with an additional HTTP endpoint to send the `TICKET` command.
This is an example utilizing some of the most important Uhaha features, such as
the tick callback, read/write/passive commands, and binary snapshots.

## Commands

```
TICKET
```

## Building

Using the source file from the examples directory, we'll build an application
named "ticket-http"

```
go build -o ticket-http main.go
```

## Running

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

The same commands as [ticket](https://github.com/tidwall/uhaha/tree/master/examples/ticket) applies, however it's possible to use an HTTP client to send the command as well.

I'll use the curl in the example below.

Connect to the leader. This will probably be the first node you created.

```
curl http://localhost:11001/ticket
```

The server would response with the following.

```
1
```

For other information check out the [Uhaha README](https://github.com/tidwall/uhaha).
