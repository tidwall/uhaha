# ticket

A fault-tolerant service for monotonically increasing tickets using the 
[Uhaha](https://github.com/tidwall/uhaha) framework

This is an example utilizing some of the most important Uhaha features, such as
the tick callback, read/write/passive commands, and binary snapshots.

## Commands

```
TICKET
```

## Building

Using the source file from the examples directory, we'll build an application
named "ticket"

```
go build -o ticket main.go
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

For other information check out the [Uhaha README](https://github.com/tidwall/uhaha).
