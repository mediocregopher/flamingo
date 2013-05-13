# Flamingo

A go framework for handling many (like millions, hopefully) of concurrent, long-held connections and
interacting with them in a pipeline-style manner.

## Usage

Here's an example echo server written using flamingo:

```go
package main

import (
    "fmt"
    "runtime"
    "os"
    "strconv"
    "flamingo"
)

func main() {

    //Use all cpus
    runtime.GOMAXPROCS(runtime.NumCPU())

    //Get port number as an integer
    args := os.Args
    if len(args) < 2 { panic("Need to supply port number") }
    portStr := args[1]
    port,_ := strconv.Atoi(portStr)

    //It's dangerous to go alone, take this flamingo
    flamingo := flamingo.New(port)
    fmt.Printf("Port created\n")

    //This routine handles all incoming connections. RecvOpen will block until a connection
    //has arrived, and returns that connection's Id and net.Conn object. The net.Conn object
    //returned so that you can get the ip address and whatever else you'd like (and set options),
    //not for reading and writing data. Please don't do that, it makes flamingo sad.
    go func() {
        for {
            id,conn := flamingo.RecvOpen()
            fmt.Printf("Got open for %d: %v\n",id,conn)
        }
    }()

    //These routines handle incoming data. All Recv* methods are thread-safe, so in this instance we
    //have 10 goroutines pulling in incoming data and sending it back to the id that sent it (echo).
    //The SendClose is commented out, but it would close the connection for that id if we wanted it to.
    for i:=0;i<10;i++ {
        go func() {
            for {
                //msg is a []byte
                id,msg := flamingo.RecvData()
                flamingo.SendData(id,msg)
                //flamingo.SendClose(id)
            }
        }()
    }

    //Finally a routine for handling connections that have closed. Note that this won't return an id
    //for a connection closed through SendClose, only for one which closes itself or is closed through
    //some network error
    go func() {
        for {
            id := flamingo.RecvClose()
            fmt.Printf("Got close for %d\n",id)
        }
    }()

    //It's important that you handle all three Recv methods in some way, since they are reading from
    //a queue on the backend, and if that queue fills up it'll block anything writing more data to it,
    //which will basically cause the whole thing to stop. So have routines calling all three, even if
    //they're just discarding the data. For example:
    //
    //go func(){ for{ flamingo.RecvOpen()  } }
    //go func(){ for{ flamingo.RecvClose() } }
    //
    //if you don't care about socket opens/closes

    select {} //Loop fo'eva
}
```

## What/How/Why?

The current documentation on how to set up a tcp-listen server with go is to simply spawn a new
goroutine for every connection that comes in. While this works for a finite amount of connections
just fine, in my testing once you go over 250k things start to fall over. The solution for this is
to have a single goroutine handle multiple connections at once. Flamingo does this and handles it
for you.

Another problem with highly concurrent systems is that of routing. For example, in a pub/sub server,
when a publish happens the server must get the list of connections from some datastore (or memory)
and loop through them. But what is that list? If the datastore is external to the program it's
probably a list of strings or integers. How to map those identifiers to socket descriptors? Flamingo
takes care of that. All sockets are identified by a single, serializable identifier, and flamingo
deals with the job of routing to an actual socket descriptor.

## Todo

Flamingo is still in development and is far from being stable or production ready. Things to-do:

* Make it possible to set socket options on the listen socket
* Make it possible to specify constant values like CONNS_PER_WORKER
* Make workers have the ability to buffer data until they find some delimiter, because with the current
  setup that's really difficult for the application to do.
* Make it possible to get status information out (number of connections, number of workers, etc...)
* Make some real documentation
* Load test this thing, haven't done so since the first iteration, of which only the core code is left
* Method for serializing the id
* Creating timeouts for socket inactivity
