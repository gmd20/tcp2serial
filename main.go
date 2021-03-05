package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"reflect"
	"time"
	"unsafe"

	"github.com/tarm/serial"
)

var (
	tcpAddress     = flag.String("l", "0.0.0.0:1234", "tcp listening address")
	serialDevice   = flag.String("s", "/dev/ttyS1", "serial device name")
	serialBaudRate = flag.Int("baudRate", 9600, "serial baudRate")
	serialDataBits = flag.Int("dataBits", 8, "serial dataBits(7 or 8)")
	serialStopBits = flag.String("stopBits", "1", "serial stopBits(1, 1.5 or 2)")
	serialParity   = flag.String("parity", "None", "serial Parity(None, Odd, Even, Mark or Space)")
	verbose        = flag.Bool("verbose", true, "log socket messages")
)

type Conn io.ReadWriteCloser

func newTcpConn() (conn Conn, err error) {
	l, err := net.Listen("tcp", *tcpAddress)
	if err != nil {
		log.Println("listen error:", err)
		return nil, err
	}

retry:
	tcpConn, err := l.Accept()
	if err != nil {
		if neterr, ok := err.(net.Error); ok && neterr.Temporary() {
			goto retry
		}
		return nil, err
	}
	addr := tcpConn.RemoteAddr().String()
	log.Printf("%v connected", addr)
	return tcpConn, nil
}

func DisableiZeroReadIsEOF(conn Conn) {
	serialPort, ok := conn.(*serial.Port)
	if !ok {
		return
	}
	p := reflect.ValueOf(serialPort)
	if !p.IsValid() {
		return
	}
	f := p.Elem().FieldByName("f")
	if !f.IsValid() {
		return
	}
	fd := f.Elem().FieldByName("pfd")
	if !fd.IsValid() {
		return
	}
	zeof := fd.FieldByName("ZeroReadIsEOF")
	if zeof.IsValid() {
		if zeof.CanSet() {
			zeof.SetBool(false)
		} else {
			ptr := (*bool)(unsafe.Pointer(zeof.UnsafeAddr()))
			*ptr = false
		}
		log.Println("serial fd.ZeroReadIsEOF is", zeof.Bool())
	}
}

func newSerialConn() (conn Conn, err error) {
	var stopBits serial.StopBits
	var parity serial.Parity

	if *serialStopBits == "1" {
		stopBits = serial.Stop1
	} else if *serialStopBits == "1.5" {
		stopBits = serial.Stop1Half
		log.Printf("Serial-StopBits 1.5 is not unsupported")
	} else if *serialStopBits == "2" {
		stopBits = serial.Stop2
	}
	if *serialParity == "None" {
		parity = serial.ParityNone
	} else if *serialParity == "Odd" {
		parity = serial.ParityOdd
	} else if *serialParity == "Even" {
		parity = serial.ParityEven
	} else if *serialParity == "Mark" {
		parity = serial.ParityMark
	} else if *serialParity == "Space" {
		parity = serial.ParitySpace
	}
	sconf := &serial.Config{
		Name:        *serialDevice,
		Baud:        *serialBaudRate,
		ReadTimeout: time.Second * 5,
		Size:        byte(*serialDataBits),
		Parity:      parity,
		StopBits:    stopBits,
	}

	sconn, err := serial.OpenPort(sconf)
	if err != nil {
		log.Println("serial OpenPort error:", err)
		return nil, err
	}
	DisableiZeroReadIsEOF(sconn)

	log.Println("Serial Port is connected")
	return sconn, nil
}

func connRelay(ctx context.Context, src Conn, dst Conn) (err error) {
	var n int
	var serr error
	var buf [4096]byte

	ctx, cancelCtx := context.WithCancel(ctx)
	defer cancelCtx()

	for {
		n, serr = src.Read(buf[0:])

		if serr != nil {
			if nerr, ok := err.(net.Error); ok && nerr.Timeout() {
				// tcp socket read timeout
				continue
			} else if os.IsTimeout(err) {
				// windows serial port read timeout
				continue
			} else {
				log.Println("recv error:", serr)
				return serr
			}
		}

		if n <= 0 {
			continue
		}

		if *verbose {
			if _, ok := src.(net.Conn); ok {
				log.Println("tcp recv:", buf[:n])
			} else {
				log.Println("serial recv:", buf[:n])
			}
		}

		if tcpConn, ok := dst.(net.Conn); ok {
			tcpConn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		}

		wn, derr := dst.Write(buf[:n])
		if derr != nil {
			log.Println("write error:", derr)
			return derr
		}
		if wn != n {
			log.Println("io error: send", wn, "recv", n)
		}
	}
}

func main() {
	flag.Parse()

	serialConn, err1 := newSerialConn()
	if err1 != nil {
		return
	}
	tcpConn, err2 := newTcpConn()
	if err2 != nil {
		return
	}

	ctx := context.Background()

	go connRelay(ctx, tcpConn, serialConn)
	go connRelay(ctx, serialConn, tcpConn)

	select {
	case <-ctx.Done():
		return
	}
}
