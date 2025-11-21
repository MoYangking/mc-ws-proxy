package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	mode             = flag.String("mode", "entry", "mode: entry | exit")
	debug            = flag.Bool("debug", false, "enable debug logging like wsmc")
	dumpBytes        = flag.Bool("dump-bytes", false, "dump hex for each proxied frame (implies -debug)")
	maxFramePayload  = flag.Int64("max-frame-payload", 65536, "maximum WebSocket payload length (similar to wsmc.maxFramePayloadLength)")
	pingInterval     = flag.Duration("ping-interval", 25*time.Second, "WebSocket ping interval to keep connections alive through CDN")

	// 入口机参数（玩家 <-> WebSocket）
	entryListenAddr  = flag.String("listen", ":25565", "TCP listen address for players, e.g. :25565")
	entryWsServerURL = flag.String("ws", "wss://mc.example.com/ws", "WebSocket server URL (Cloudflare hostname), e.g. wss://mc.example.com/ws")
	entrySkipTLS     = flag.Bool("skip-tls-verify", true, "skip TLS certificate verification when dialing entry WebSocket (insecure)")

	// 出口机参数（WebSocket <-> 本地MC）
	exitListenAddr = flag.String("exit-listen", ":8080", "WebSocket listen address on exit server, e.g. :8080")
	exitTargetAddr = flag.String("exit-target", "127.0.0.1:25565", "TCP target address (Minecraft server), e.g. 127.0.0.1:25565")
)

const (
	tcpReadTimeout  = 120 * time.Second
	tcpWriteTimeout = 30 * time.Second
	wsReadTimeout   = 60 * time.Second
	closeWait       = 2 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// 如需限制来源（只允许 Cloudflare IP），可以在这里做检查
		return true
	},
}

func main() {
	flag.Parse()

	switch *mode {
	case "entry":
		runEntry()
	case "exit":
		runExit()
	default:
		log.Fatalf("unknown mode: %s (must be entry or exit)", *mode)
	}
}

///////////////////////
//  入口机：玩家TCP <-> WebSocket
///////////////////////

func runEntry() {
	ln, err := net.Listen("tcp", *entryListenAddr)
	if err != nil {
		log.Fatal("TCP listen error:", err)
	}
	log.Printf("[ENTRY] Listening on %s, forwarding to %s\n", *entryListenAddr, *entryWsServerURL)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println("[ENTRY] Accept error:", err)
			continue
		}
		log.Println("[ENTRY] New player from", conn.RemoteAddr())
		go handleEntryConn(conn)
	}
}

func handleEntryConn(tcpConn net.Conn) {
	defer tcpConn.Close()
	if c, ok := tcpConn.(*net.TCPConn); ok {
		c.SetNoDelay(true)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: *entrySkipTLS,
		},
	}

	ws, _, err := dialer.Dial(*entryWsServerURL, nil)
	if err != nil {
		log.Println("[ENTRY] Dial WS backend error:", err)
		return
	}
	log.Println("[ENTRY] Connected to WS backend", *entryWsServerURL)
	defer ws.Close()

	bridgeTCPAndWS(tcpConn, ws, "[ENTRY]")

	log.Println("[ENTRY] Connection closed for player", tcpConn.RemoteAddr())
}

///////////////////////
//  出口机：WebSocket <-> 本地MC TCP
///////////////////////

func runExit() {
	http.HandleFunc("/ws", handleExitWS)

	log.Printf("[EXIT] Listening on %s (WebSocket), forwarding to %s\n", *exitListenAddr, *exitTargetAddr)
	err := http.ListenAndServe(*exitListenAddr, nil)
	if err != nil {
		log.Fatal("[EXIT] ListenAndServe error:", err)
	}
}

func handleExitWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("[EXIT] WebSocket upgrade error:", err)
		return
	}
	log.Println("[EXIT] New WS connection from", r.RemoteAddr)
	defer ws.Close()

	tcpConn, err := net.Dial("tcp", *exitTargetAddr)
	if err != nil {
		log.Println("[EXIT] Dial TCP target error:", err)
		return
	}
	log.Println("[EXIT] Connected to TCP target", *exitTargetAddr)
	defer tcpConn.Close()

	if c, ok := tcpConn.(*net.TCPConn); ok {
		c.SetNoDelay(true)
	}

	bridgeTCPAndWS(tcpConn, ws, "[EXIT]")

	log.Println("[EXIT] WS connection closed from", r.RemoteAddr)
}

///////////////////////
//  通用复制函数（参考 wsmc WebSocketHandler）
///////////////////////

func bridgeTCPAndWS(tcpConn net.Conn, ws *websocket.Conn, tag string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ws.SetReadLimit(*maxFramePayload)
	ws.SetReadDeadline(time.Now().Add(wsReadTimeout))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(wsReadTimeout))
		return nil
	})

	errCh := make(chan error, 3)
	var wg sync.WaitGroup
	var wsWriteMu sync.Mutex

	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- copyTCPToWS(ctx, tcpConn, ws, &wsWriteMu, tag)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- copyWSToTCP(ctx, ws, tcpConn, tag)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		errCh <- wsPingLoop(ctx, ws, &wsWriteMu, tag)
	}()

	firstErr := <-errCh
	cancel()

	_ = tcpConn.SetDeadline(time.Now())
	_ = ws.SetReadDeadline(time.Now())

	wsWriteMu.Lock()
	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(closeWait))
	wsWriteMu.Unlock()

	_ = ws.Close()
	_ = tcpConn.Close()

	wg.Wait()

	if firstErr != nil && !errors.Is(firstErr, context.Canceled) && !errors.Is(firstErr, io.EOF) {
		log.Println(tag, "bridge closed:", firstErr)
	}
}

func copyTCPToWS(ctx context.Context, tcp net.Conn, ws *websocket.Conn, wsMu *sync.Mutex, tag string) error {
	buf := make([]byte, 8192)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		_ = tcp.SetReadDeadline(time.Now().Add(tcpReadTimeout))
		n, err := tcp.Read(buf)
		if err != nil {
			return fmt.Errorf("%s TCP read: %w", tag, err)
		}
		if n <= 0 {
			continue
		}

		slice := buf[:n]
		if *debug || *dumpBytes {
			log.Printf("%s TCP->WS (%d)", tag, n)
		}
		if *dumpBytes {
			dumpHex(slice)
		}

		wsMu.Lock()
		_ = ws.SetWriteDeadline(time.Now().Add(tcpWriteTimeout))
		err = ws.WriteMessage(websocket.BinaryMessage, slice)
		wsMu.Unlock()
		if err != nil {
			return fmt.Errorf("%s WS write: %w", tag, err)
		}
	}
}

func copyWSToTCP(ctx context.Context, ws *websocket.Conn, tcp net.Conn, tag string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgType, data, err := ws.ReadMessage()
		if err != nil {
			return fmt.Errorf("%s WS read: %w", tag, err)
		}

		switch msgType {
		case websocket.BinaryMessage:
			if *debug || *dumpBytes {
				log.Printf("%s WS->TCP (%d)", tag, len(data))
			}
			if *dumpBytes {
				dumpHex(data)
			}

			_ = tcp.SetWriteDeadline(time.Now().Add(tcpWriteTimeout))
			if _, err := tcp.Write(data); err != nil {
				return fmt.Errorf("%s TCP write: %w", tag, err)
			}
		case websocket.CloseMessage:
			return io.EOF
		case websocket.TextMessage:
			// ignore text frames as in wsmc
			continue
		default:
			if *debug {
				log.Printf("%s unsupported WS frame type: %d", tag, msgType)
			}
		}
	}
}

func wsPingLoop(ctx context.Context, ws *websocket.Conn, wsMu *sync.Mutex, tag string) error {
	ticker := time.NewTicker(*pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			wsMu.Lock()
			err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(tcpWriteTimeout))
			wsMu.Unlock()
			if err != nil {
				return fmt.Errorf("%s WS ping: %w", tag, err)
			}
		}
	}
}

func dumpHex(data []byte) {
	const maxPerLine = 32
	for i := 0; i < len(data); i += maxPerLine {
		end := i + maxPerLine
		if end > len(data) {
			end = len(data)
		}
		line := data[i:end]
		out := make([]byte, 0, len(line)*3)
		for _, b := range line {
			out = append(out, fmt.Sprintf("%02X ", b)...)
		}
		log.Printf("%s", string(out))
	}
}
