package flamingo

import (
    "fmt"
    "net"
    "strconv"
    "sync"
    "time"
    "bytes"
    "io"
)

type Id uint64

type connection struct {
    conn *net.Conn
    cid  Id
}

type command interface {
    ConnId() Id
    Type() int
}

//Types of commands
const (
    WRITE = iota
    CLOSE
)

type message struct {
    cid Id
    msg []byte
}

//A message being interpreted as a command is always going to be a write command,
//the data in the message being what is written
func (m *message) Type() int  { return WRITE }
func (m *message) ConnId() Id { return m.cid }

type closeCommand Id
func (c *closeCommand) Type() int  { return CLOSE }
func (c *closeCommand) ConnId() Id { return Id(*c) }

type commandRouter struct {
    sync.RWMutex
    m map[Id]chan command
}

func newCommandRouter() *commandRouter {
    return &commandRouter{
        m: map[Id]chan command{},
    }
}

type Opts struct {
    Port            int
    ActivityTimeout time.Duration
    BufferSize      int
    BufferTillDelim bool
    Delim           byte
}

type Flamingo struct {
    //These are going to be read from by the application
    globalOpenCh  chan *connection
    globalReadCh  chan *message
    globalCloseCh chan *connection

    //For private use
    incomingCh chan *connection
    cmdR       *commandRouter
    opts       *Opts
}

func New(opts Opts) *Flamingo {

    //Default options
    if opts.BufferSize == 0 { opts.BufferSize = 1024 }

    //Make our listen socket
    server, err := net.Listen("tcp", ":"+strconv.Itoa(opts.Port))
    if server == nil {
        panic("couldn't start listening: " + err.Error())
    }

    flamingo := &Flamingo{
        globalOpenCh:  make(chan *connection, 2048),
        globalReadCh:  make(chan *message, 2048),
        globalCloseCh: make(chan *connection, 2048),
        incomingCh:    make(chan *connection),
        cmdR:          newCommandRouter(),
        opts:          &opts,
    }

    //Spawn a routine to listen for new connections
    makeAcceptors(flamingo, server)

    go distributor(flamingo)

    return flamingo
}

func (f *Flamingo) RecvOpen() (Id, net.Conn) {
    c := <-f.globalOpenCh
    return c.cid, *c.conn
}

func (f *Flamingo) RecvData() (Id, []byte) {
    msg := <-f.globalReadCh
    return msg.cid, msg.msg
}

func (f *Flamingo) SendData(cid Id, msg []byte) {
    wrmsg := message{cid, msg}
    f.routeCommand(cid, &wrmsg)
}

func (f *Flamingo) RecvClose() Id {
    c := <-f.globalCloseCh
    return c.cid
}

func (f *Flamingo) SendClose(cid Id) {
    cmsg := closeCommand(cid)
    f.routeCommand(cid, &cmsg)
}

func (f *Flamingo) routeCommand(cid Id, c command) {
    f.cmdR.RLock()
    f.cmdR.m[cid] <- c
    f.cmdR.RUnlock()
}

func makeAcceptors(f *Flamingo, listener net.Listener) {

    //Create routine to atomically get incremental numbers
    cidCh := make(chan Id)
    go func() {
        var i Id = 0
        for {
            cidCh <- i
            i++
        }
    }()

    for j := 0; j < 10; j++ {
        go func() {
            for {
                client, err := listener.Accept()
                if client == nil {
                    fmt.Printf("accept failed" + err.Error() + "\n")
                    continue
                }

                cid := <-cidCh
                connection := &connection{&client, cid}

                f.incomingCh <- connection
                f.globalOpenCh <- connection
            }
        }()
    }

}

func distributor(f *Flamingo) {
    for conn := range f.incomingCh {

        workerCommandCh := make(chan command, 20)
        go connWorker(f, conn, workerCommandCh)
        f.cmdR.Lock()
        f.cmdR.m[conn.cid] = workerCommandCh
        f.cmdR.Unlock()

    }
}

type readChRet struct {
    msg []byte
    err error
}
func connWorker(f *Flamingo, conn *connection, commandCh chan command) {

    workerReadCh  := make(chan *readChRet)
    readMore := true
    readBuf := new(bytes.Buffer)
    for {

        //readMore keeps track of whether or not a routine is already reading
        //off the connection. If there isn't one we make another
        if readMore {
            go func(){
                var ret readChRet
                buf := make([]byte,f.opts.BufferSize)
                bcount, err := (*conn.conn).Read(buf)
                if err != nil {
                    ret = readChRet{nil,err}
                } else if bcount > 0 {
                    ret = readChRet{buf,nil}
                } else {
                    ret = readChRet{nil,nil}
                }
                workerReadCh <- &ret
            }()
            readMore = false
        }

        //TODO it may be possible for someone to generate a lot of superfluous
        //     routines if the timeout is set high and a lot of data is sent
        var timeoutChan <- chan time.Time
        if f.opts.ActivityTimeout > 0 {
            timeoutChan = time.After(f.opts.ActivityTimeout)
        }

        select {

        //If we pull a command off we decode it and act accordingly
        case command := <-commandCh:
            switch command.Type() {
            case WRITE:
                msg := command.(*message).msg
                (*conn.conn).Write(msg)

            case CLOSE:
                closeAndRemove(f,conn)
                return
            }


        //If the goroutine doing the reading gets data we check it for an error
        //and send it to the globalReadCh to be handled
        case rcr := <-workerReadCh:
            readMore = true
            msg,err := rcr.msg,rcr.err
            if err != nil {
                closeAndRemove(f,conn)
                f.globalCloseCh <- conn
                return
            } else if msg != nil {
                if f.opts.BufferTillDelim {
                    readBuf.Write(msg)
                    for {
                        fullMsg,err := readBuf.ReadBytes(f.opts.Delim)
                        if err == io.EOF {
                            //We got to the end of the buffer without finding a delim,
                            //write back what we did find so it can be searched the next time
                            readBuf.Write(fullMsg)
                            break
                        } else {
                            f.globalReadCh <- &message{conn.cid,fullMsg}
                        }
                    }
                } else {
                    f.globalReadCh <- &message{conn.cid,msg}
                }
            }

        case <-timeoutChan:
            closeAndRemove(f,conn)
            f.globalCloseCh <- conn
            return

        }
    }
}

//Closes a connection and removes it from the router
func closeAndRemove(f *Flamingo, conn *connection) {
    (*conn.conn).Close()
    f.cmdR.Lock()
    delete(f.cmdR.m,conn.cid)
    f.cmdR.Unlock()
}
