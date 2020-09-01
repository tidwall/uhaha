# kvdb

A fault-tolerant key value database using the 
[Uhaha](https://github.com/tidwall/uhaha) framework

This is an example utilizing some of the most important Uhaha features, such as
the tick callback, read/write/passive commands, argument filtering, and binary snapshots.

## Commands

```
SET key value [EX seconds]
DEL key [key ...]
GET key
KEYS pattern
DBSIZE
MONITOR
SETRANDQUOTE key
```

## Building

Using the source file from the examples directory, we'll build an application
named "kvdb"

```
go build -o kvdb main.go
```

## Running

It's ideal to have three, five, or seven nodes in your cluster.

Let's create the first node.

```
./kvdb -n 1 -a :11001
```

This will create a node named 1 and bind the address to :11001

Now let's create two more nodes and add them to the cluster.

```
./kvdb -n 2 -a :11002 -j :11001
./kvdb -n 3 -a :11003 -j :11001
```

Now we have a fault-tolerant three node cluster up and running.

## Using

You can use any Redis compatible client, such as the redis-cli, telnet, 
or netcat.

I'll use the redis-cli in the example below.

Connect to the leader. This will probably be the first node you created.

```
redis-cli -p 11001
```

Send the server some commands and set a key value.

```
> SET hello world
OK
> GET hello
"world"
```

For other information check out the [Uhaha README](https://github.com/tidwall/uhaha).
