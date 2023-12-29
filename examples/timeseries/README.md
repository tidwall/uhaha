# timeseries

A fault-tolerant timeseries database using the 
[Uhaha](https://github.com/tidwall/uhaha) framework

## Commands

```sh
WRITE measurement timestamp fields # write a point
QUERY measurement start end limit  # read points
RETAIN [duration]                  # set or get retention duration
STATS                              # returns database stats
```

examples: 

```sh
WRITE cpu now hello=jello             # write a point with at time
WRITE cpu 1598907601555488000 hi=sky  # write a point with unix nanoseconds
QUERY cpu -5m now 1000                # return up to 1000 points from five minutes ago
QUERY cpu -10m -5m 1000               # return up to 1000 points from ten to five minutes ago
RETAIN 1h                             # retain points for up to one hour
```

## Building

Using the source file from the examples directory, we'll build an application
named "timeseries"

```
go build -o timeseries main.go
```

## Running

It's ideal to have three, five, or seven nodes in your cluster.

Let's create the first node.

```
./timeseries -n 1 -a :11001
```

This will create a node named 1 and bind the address to :11001

Now let's create two more nodes and add them to the cluster.

```
./timeseries -n 2 -a :11002 -j :11001
./timeseries -n 3 -a :11003 -j :11001
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
> WRITE cpu now hello=jello
OK
> WRITE cpu now hi=sky
OK
> query cpu 0 now 1000
1) "cpu 1598907970926047001 hello=jello"
2) "cpu 1598907998045460001 hi=sky"
```

For other information check out the [Uhaha README](https://github.com/tidwall/uhaha).
