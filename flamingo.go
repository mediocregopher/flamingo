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

type Connection struct {
    conn     *net.Conn
    id       uint64
}

type MessageStruct struct {
    id  uint64
    msg []byte
}

type Message interface {
    Id()      uint64
    Command() int
}

type ReadMessage  MessageStruct
type WriteMessage MessageStruct

//Types of commands
const (
    WRITE = iota
    CLOSE
)
func (m *WriteMessage) Command() int    { return WRITE }
func (m *WriteMessage) Id()      uint64 { return m.id  }

type Application struct {
    globalReaderCh chan *ReadMessage
    commandRouter  *[]chan Message
}

func NewApp(port int) (*Application) {
    //Make our listen socket
    server, err := net.Listen( "tcp", ":" + strconv.Itoa(port) )
    if server == nil { panic("couldn't start listening: " + err.Error()) }

    commandRouter := make([]chan Message, 0, ESTIMATED_CONNS/CONNS_PER_WORKER)

    //Spawn a routine to listen for new connections
    incomingCh := accepter(server)

    //The channel that we can read from to get data from connections
    globalReaderCh := make(chan *ReadMessage,2000)

    go distributor(&commandRouter,incomingCh,globalReaderCh)

    return &Application{ globalReaderCh, &commandRouter }
}

func (a *Application) RecvData() (uint64,[]byte) {
    msg := <- a.globalReaderCh
    return msg.id, msg.msg
}

func (a *Application) SendData(id uint64, msg []byte) {
    wrmsg := &WriteMessage{ id, msg }
    (*a.commandRouter)[ id / CONNS_PER_WORKER ] <- wrmsg
}

func accepter(listener net.Listener) chan *Connection {
    ch := make(chan *Connection)

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
                connection := &Connection{ &client, i }

                ch <- connection
            }
        }()
    }

    return ch
}

func distributor(commandRouter *[]chan Message, incomingCh chan *Connection, globalReaderCh chan *ReadMessage) {
    for {
        commandCh        := make(chan Message,20)
        workerIncomingCh := make(chan *Connection,20)

        go worker(workerIncomingCh,globalReaderCh,commandCh)
        *commandRouter = append(*commandRouter,commandCh)

        //This works, but I think it's a bottleneck
        for connCount := 0; connCount < CONNS_PER_WORKER; connCount++ {
            workerIncomingCh <- <- incomingCh
        }

        workerIncomingCh <- nil
    }
}

func worker(incomingCh chan *Connection, globalReaderCh chan *ReadMessage, commandCh chan Message) {

    connL := make([]*Connection,CONNS_PER_WORKER)
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
                    i := command.Id() % CONNS_PER_WORKER
                    if command.Command() == WRITE {
                        conn := connL[i]
                        msg := command.(*WriteMessage).msg
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
            return
        }
    }
}

func tryRead(globalReaderCh chan *ReadMessage, connection *Connection) error {
    conn := connection.conn

    (*conn).SetReadDeadline(time.Now())

    buf := make([]byte, 1024)
    bcount, err := (*conn).Read(buf)
    if err == io.EOF {
        return err
    } else if bcount > 0 {
        msg := &ReadMessage{ connection.id, buf }
        globalReaderCh <- msg
    }
    return nil
}

