package flamingo

import (
    "net"
    "fmt"
    "time"
    "container/list"
    "io"
    "strconv"
)

const CONN_TIMEOUT_CHECK = 10 //seconds
const CONNS_PER_WORKER = 500

type Connection struct {
    conn     *net.Conn
    id       uint64
    writerCh chan *WriteMessage
}

type Message struct {
    connection    *Connection
    msg           []byte
}

type ReadMessage  Message
type WriteMessage Message

func New(port int) (chan *ReadMessage) {
    //Make our listen socket
    server, err := net.Listen( "tcp", ":" + strconv.Itoa(port) )
    if server == nil { panic("couldn't start listening: " + err.Error()) }

    //Spawn a routine to listen for new connections
    incomingCh := accepter(server)

    //The channel that we can read from to get data from connections
    globalReaderCh := make(chan *ReadMessage,2000)

    go distributor(incomingCh,globalReaderCh)

    return globalReaderCh
}

func (msg *ReadMessage) Respond(response *[]byte) {
    msg.connection.writerCh <- &WriteMessage{msg.connection,*response}
}

func (msg *ReadMessage)  Data() *[]byte { return &msg.msg }
func (msg *WriteMessage) Data() *[]byte { return &msg.msg }

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
                connection := &Connection{ &client, i, nil }

                ch <- connection
            }
        }()
    }

    return ch
}

func distributor(incomingCh chan *Connection, globalReaderCh chan *ReadMessage) {
    for {
        writerCh         := make(chan *WriteMessage,20)
        workerIncomingCh := make(chan *Connection,20)

        go reader(workerIncomingCh,globalReaderCh,writerCh)
        go writer(writerCh)

        //This works, but I think it's a bottleneck
        for connCount := 0; connCount < CONNS_PER_WORKER; connCount++ {
            workerIncomingCh <- <- incomingCh
        }

        workerIncomingCh <- nil
    }
}

func reader(incomingCh chan *Connection, globalReaderCh chan *ReadMessage, writerCh chan *WriteMessage) {

    connL := list.New()
    stillInUse := true

    for {
        for e := connL.Front(); e != nil; e = e.Next() {
            connection := e.Value.(*Connection)
            err := tryRead(globalReaderCh,connection)
            if err != nil { connL.Remove(e) }
        }

        for loop := true; loop == true; {
            select {
                case connection := <-incomingCh:
                    if connection == nil {
                        stillInUse = false
                    } else {
                        connection.writerCh = writerCh
                        connL.PushBack(connection)
                    }
                default:
                    loop = false
            }
        }

        //If we're not in use (no incoming conns) and there's none
        //connected we're done. Tell the writer we're done too
        if !stillInUse && connL.Len() == 0 {
            //TODO it's possible that the user's routines may write here after the nil,
            //     do we care? At this point all connections should be closed so it's
            //     probably moot
            writerCh <- nil
            return
        }
    }
}

func writer(writerCh chan *WriteMessage) {
    for {
        msg := <-writerCh
        if msg == nil {
            return
        } else {
            (*msg.connection.conn).Write(msg.msg) //Don't care about errors, no way to report them
                                                  //anyway
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
        msg := &ReadMessage{ connection, buf }
        globalReaderCh <- msg
    }
    return nil
}

