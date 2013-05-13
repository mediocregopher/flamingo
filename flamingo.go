package flamingo

import (
    "net"
    "fmt"
    "time"
    "io"
    "strconv"
    "sync"
)

const CONN_TIMEOUT_CHECK = 10 //seconds
const CONNS_PER_WORKER = 500

type Id uint64
type workerId uint64

type connection struct {
    conn     *net.Conn
    cid      Id
}

type command interface {
    ConnId()  Id
    Type()    int
}

//Types of commands
const (
    WRITE = iota
    CLOSE
)

type message struct {
    cid  Id
    msg []byte
}

//A message being interpreted as a command is always going to be a write command,
//the data in the message being what is written
func (m *message) Type()   int { return WRITE }
func (m *message) ConnId() Id  { return m.cid  }

type closeCommand Id
func (c *closeCommand) Type()   int { return CLOSE }
func (c *closeCommand) ConnId() Id  { return Id(*c) }

type commandRouter struct {
    sync.RWMutex
    m map[workerId]chan command
}
func newCommandRouter() (*commandRouter) {
    return &commandRouter{
        m: map[workerId]chan command{},
    }
}

type Flamingo struct {
    //These are going to be read from by the application
    globalOpenCh  chan *connection
    globalReadCh  chan *message
    globalCloseCh chan *connection

    //For private use
    incomingCh   chan *connection
    cmdR         *commandRouter
}

func New(port int) (*Flamingo) {
    //Make our listen socket
    server, err := net.Listen( "tcp", ":" + strconv.Itoa(port) )
    if server == nil { panic("couldn't start listening: " + err.Error()) }

    flamingo := &Flamingo{ globalOpenCh:  make(chan *connection,2000),
                           globalReadCh:  make(chan *message,2000),
                           globalCloseCh: make(chan *connection,2000),
                           incomingCh:    make(chan *connection),
                           cmdR:          newCommandRouter() }

    //Spawn a routine to listen for new connections
    makeAcceptors(flamingo,server)

    go distributor(flamingo)

    return flamingo
}

func (f *Flamingo) RecvOpen() (Id,net.Conn) {
    c := <- f.globalOpenCh
    return c.cid, *c.conn
}

func (f *Flamingo) RecvData() (Id,[]byte) {
    msg := <- f.globalReadCh
    return msg.cid, msg.msg
}

func (f *Flamingo) SendData(cid Id, msg []byte) {
    wrmsg := message{ cid, msg }
    f.routeCommand(cid,&wrmsg)
}

func (f *Flamingo) RecvClose() Id {
    c := <- f.globalCloseCh
    return c.cid
}

func (f *Flamingo) SendClose(cid Id) {
    cmsg := closeCommand(cid)
    f.routeCommand(cid,&cmsg)
}

func (f *Flamingo) routeCommand(cid Id, c command) {
    f.cmdR.RLock()
    f.cmdR.m[ workerId( cid / CONNS_PER_WORKER ) ] <- c
    f.cmdR.RUnlock()
}

func makeAcceptors(f *Flamingo, listener net.Listener) {

    //Create routine to atomically get incremental numbers
    cidCh := make(chan Id)
    go func() {
        var i Id = 0
        for { cidCh <- i; i++ }
    }()


    for j:=0; j<10; j++ {
        go func() {
            for {
                client, err := listener.Accept()
                if client == nil { fmt.Printf("accept failed"+err.Error()+"\n"); continue }

                cid := <-cidCh
                connection := &connection{ &client, cid }

                f.incomingCh <- connection
                f.globalOpenCh <- connection
            }
        }()
    }

}

func distributor(f *Flamingo) {
    for wid := workerId(0);;wid++ {
        workerCommandCh  := make(chan command,20)
        workerIncomingCh := make(chan *connection,20)

        go worker(f,wid,workerIncomingCh,workerCommandCh)
        f.cmdR.Lock()
        f.cmdR.m[wid] = workerCommandCh
        f.cmdR.Unlock()

        //This works, but I think it's a bottleneck
        for connCount := 0; connCount < CONNS_PER_WORKER; connCount++ {
            workerIncomingCh <- <- f.incomingCh
        }

        workerIncomingCh <- nil
    }
}

func worker(f *Flamingo, wid workerId, incomingCh chan *connection, commandCh chan command) {

    globalReadCh  := f.globalReadCh

    connL := make([]*connection,CONNS_PER_WORKER)
    connSlotsUsed := 0

    stillInUse := true

    for {
        for i,conn := range connL {
            if (conn != nil) {
                err := tryRead(globalReadCh,conn)
                if err != nil {
                    (*conn.conn).Close()
                    connL[i] = nil
                    connSlotsUsed--
                    f.globalCloseCh <- conn
                }
            }
        }

        for loop := true; loop == true; {
            select {
                case conn := <-incomingCh:
                    if conn == nil {
                        stillInUse = false
                    } else {
                        connL[ conn.cid % CONNS_PER_WORKER ] = conn
                        connSlotsUsed++
                    }

                case command := <- commandCh:
                    ci := command.ConnId() % CONNS_PER_WORKER
                    switch command.Type() {
                        case WRITE:
                            conn := connL[ci]
                            msg := command.(*message).msg
                            (*conn.conn).Write(msg)

                        case CLOSE:
                            conn := connL[ci]
                            (*conn.conn).Close()
                            connL[ci] = nil
                            connSlotsUsed--
                    }
                default:
                    loop = false
            }
        }

        //If we're not in use (no incoming conns) and there's none
        //connected we're done. Tell the writer we're done too
        if !stillInUse && connSlotsUsed == 0 {
            //TODO it's possible that the user's routines may write here after the nil,
            //     do we care? At this point all connections should be closed so it's
            //     probably moot
            f.cmdR.Lock()
            delete(f.cmdR.m,wid)
            f.cmdR.Unlock()
            return
        }
    }
}

func tryRead(globalReadCh chan *message, c *connection) error {
    conn := c.conn

    (*conn).SetReadDeadline(time.Now())

    buf := make([]byte, 1024)
    bcount, err := (*conn).Read(buf)
    if err == io.EOF {
        return err
    } else if bcount > 0 {
        msg := &message{ c.cid, buf }
        globalReadCh <- msg
    }
    return nil
}

