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
    "time"
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
    flamingo := flamingo.New(flamingo.Opts{
        Port: port,

        //How long to keep a connection alive when it hasn't had any new data
        //and no commands called on it. Defaults to zero, meaning no timeout
        ActivityTimeout: 5 * time.Second,

        //Max number of bytes to read off the socket at a time. Defaults to 1024,
        //doesn't really need to be set unless you have a specific reason
        BufferSize: 1024,

        //If we want to buffer data until we hit a specific byte, set to true and
        //and specify the byte we want. Messages read from RecvData will include
        //the trailing delimeter
        BufferTillDelim: true,
        Delim: '\n',
    })
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

One of problems with highly concurrent systems is that of routing. For example, in a pub/sub server,
when a publish happens the server must get the list of connections from some datastore (or memory)
and loop through them. But what is that list? If the datastore is external to the program it's
probably a list of strings or integers. How to map those identifiers to socket descriptors? Flamingo
takes care of that. All sockets are identified by a single, serializable identifier, and flamingo
deals with the job of routing to an actual socket descriptor.

## Limitations

Currently the most obvious limitation of flamingo is in socket data buffering. If the messages you're
going to be receiving on your sockets are anything more complicated then data ending in a specific
delimiter you're going to have a difficult time. I'm still trying to think of the best solution for
this, and will absolutely take suggestions if anyone has any.

## Todo

Flamingo is still in development and is far from being stable or production ready. Things to-do:

* Make it possible to set socket options on the listen socket
* Make workers have the ability to buffer data until they find some delimiter, because with the current
  setup that's really difficult for the application to do.
* Make it possible to get status information out (number of connections, etc...)
* Make some real documentation
* Load test this thing, haven't done so since the first iteration, of which only the core code is left
* Method for serializing the id
