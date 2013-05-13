package flamingo

import (
    "net"
    "fmt"
    "time"
    "io"
    "strconv"
)

const CONN_TIMEOUT_CHECK = 10 //seconds
const CONNS_PER_WORKER = 500
const ESTIMATED_CONNS = 1e6

type Id uint64
type workerId uint64

type connection struct {
    conn     *net.Conn
    id       Id
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
    id  Id
    msg []byte
}

//A message being interpreted as a command is always going to be a write command,
//the data in the message being what is written
func (m *message) Type()   int { return WRITE }
func (m *message) ConnId() Id  { return m.id  }

type closeCommand Id
func (c *closeCommand) Type()   int { return CLOSE }
func (c *closeCommand) ConnId() Id  { return Id(*c) }

type Flamingo struct {
    globalReadCh chan *message
    commandRouter  map[workerId]chan command
}

func New(port int) (*Flamingo) {
    //Make our listen socket
    server, err := net.Listen( "tcp", ":" + strconv.Itoa(port) )
    if server == nil { panic("couldn't start listening: " + err.Error()) }

    commandRouter := map[workerId]chan command{}

    //Spawn a routine to listen for new connections
    incomingCh := makeAcceptors(server)

    //The channel that we can read from to get data from connections
    globalReadCh := make(chan *message,2000)

    flamingo := &Flamingo{ globalReadCh, commandRouter }
    go distributor(flamingo,incomingCh)

    return flamingo
}

func (f *Flamingo) RecvData() (Id,[]byte) {
    msg := <- f.globalReadCh
    return msg.id, msg.msg
}

func (f *Flamingo) SendData(id Id, msg []byte) {
    wrmsg := message{ id, msg }
    f.routeCommand(id,&wrmsg)
}

func (f *Flamingo) Close(id Id) {
    cmsg := closeCommand(id)
    f.routeCommand(id,&cmsg)
}

func (f *Flamingo) routeCommand(id Id, c command) {
    f.commandRouter[ workerId( id / CONNS_PER_WORKER ) ] <- c
}

func makeAcceptors(listener net.Listener) chan *connection {
    ch := make(chan *connection)

    //Create routine to atomically get incremental numbers
    ich := make(chan uint64)
    var i uint64 = 0
    go func() {
        for { ich <- i; i++ }
    }()


    for j:=0; j<10; j++ {
        go func() {
            for {
                client, err := listener.Accept()
                if client == nil { fmt.Printf("accept failed"+err.Error()+"\n"); continue }

                i := Id(<-ich)
                connection := &connection{ &client, i }

                ch <- connection
            }
        }()
    }

    return ch
}

func distributor(f *Flamingo, incomingCh chan *connection) {
    commandRouter := f.commandRouter
    for i := workerId(0); ; i++ {
        commandCh        := make(chan command,20)
        workerIncomingCh := make(chan *connection,20)

        go worker(f,i,workerIncomingCh,commandCh)
        commandRouter[i] = commandCh

        //This works, but I think it's a bottleneck
        for connCount := 0; connCount < CONNS_PER_WORKER; connCount++ {
            workerIncomingCh <- <- incomingCh
        }

        workerIncomingCh <- nil
    }
}

func worker(f *Flamingo, i workerId, incomingCh chan *connection, commandCh chan command) {

    globalReadCh  := f.globalReadCh
    commandRouter := f.commandRouter

    connL := make([]*connection,CONNS_PER_WORKER)
    connSlotsUsed := 0

    stillInUse := true

    for {
        for i,conn := range connL {
            if (conn != nil) {
                err := tryRead(globalReadCh,conn)
                if err != nil { connL[i] = nil; connSlotsUsed--; }
            }
        }

        for loop := true; loop == true; {
            select {
                case connection := <-incomingCh:
                    if connection == nil {
                        stillInUse = false
                    } else {
                        connL[ connection.id % CONNS_PER_WORKER ] = connection
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
            delete(commandRouter,i)
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
        msg := &message{ c.id, buf }
        globalReadCh <- msg
    }
    return nil
}

