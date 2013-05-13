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

type connection struct {
    conn     *net.Conn
    id       uint64
}

type message struct {
    id  uint64
    msg []byte
}

type command interface {
    ConnId()  uint64
    Type()    int
}

//Types of commands
const (
    WRITE = iota
    CLOSE
)

//A message being interpreted as a command is always going to be a write command,
//the data in the message being what is written
func (m *message) Type()   int    { return WRITE }
func (m *message) ConnId() uint64 { return m.id  }

type Flamingo struct {
    globalReaderCh chan *message
    commandRouter  map[uint64]chan command
}

func New(port int) (*Flamingo) {
    //Make our listen socket
    server, err := net.Listen( "tcp", ":" + strconv.Itoa(port) )
    if server == nil { panic("couldn't start listening: " + err.Error()) }

    commandRouter := map[uint64]chan command{}

    //Spawn a routine to listen for new connections
    incomingCh := makeAcceptors(server)

    //The channel that we can read from to get data from connections
    globalReaderCh := make(chan *message,2000)

    flamingo := &Flamingo{ globalReaderCh, commandRouter }
    go distributor(flamingo,incomingCh)

    return flamingo
}

func (f *Flamingo) RecvData() (uint64,[]byte) {
    msg := <- f.globalReaderCh
    return msg.id, msg.msg
}

func (f *Flamingo) SendData(id uint64, msg []byte) {
    wrmsg := &message{ id, msg }
    f.commandRouter[ id / CONNS_PER_WORKER ] <- wrmsg
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

                i := <-ich
                connection := &connection{ &client, i }

                ch <- connection
            }
        }()
    }

    return ch
}

func distributor(f *Flamingo, incomingCh chan *connection) {
    commandRouter := f.commandRouter
    for i := uint64(0); ; i++ {
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

func worker(f *Flamingo, i uint64, incomingCh chan *connection, commandCh chan command) {

    globalReaderCh := f.globalReaderCh
    commandRouter  := f.commandRouter

    connL := make([]*connection,CONNS_PER_WORKER)
    connSlotsUsed := 0

    stillInUse := true

    for {
        for i,conn := range connL {
            if (conn != nil) {
                err := tryRead(globalReaderCh,conn)
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
                    if command.Type() == WRITE {
                        conn := connL[ci]
                        msg := command.(*message).msg
                        (*conn.conn).Write(msg)
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

func tryRead(globalReaderCh chan *message, c *connection) error {
    conn := c.conn

    (*conn).SetReadDeadline(time.Now())

    buf := make([]byte, 1024)
    bcount, err := (*conn).Read(buf)
    if err == io.EOF {
        return err
    } else if bcount > 0 {
        msg := &message{ c.id, buf }
        globalReaderCh <- msg
    }
    return nil
}

