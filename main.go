package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var (
	mode = flag.String("mode", "entry", "mode: entry | exit")

	// 入口机参数（玩家 <-> WebSocket）
	entryListenAddr  = flag.String("listen", ":25565", "TCP listen address for players, e.g. :25565")
	entryWsServerURL = flag.String("ws", "wss://mc.example.com/ws", "WebSocket server URL (Cloudflare hostname), e.g. wss://mc.example.com/ws")

	// 出口机参数（WebSocket <-> 本地MC）
	exitListenAddr = flag.String("exit-listen", ":8080", "WebSocket listen address on exit server, e.g. :8080")
	exitTargetAddr = flag.String("exit-target", "127.0.0.1:25565", "TCP target address (Minecraft server), e.g. 127.0.0.1:25565")
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
	}

	ws, _, err := dialer.Dial(*entryWsServerURL, nil)
	if err != nil {
		log.Println("[ENTRY] Dial WS backend error:", err)
		return
	}
	log.Println("[ENTRY] Connected to WS backend", *entryWsServerURL)
	defer ws.Close()

	ws.SetReadLimit(1 << 20) // 1MB
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 启动双向转发：TCP->WS 和 WS->TCP
	go copyTCPToWS(tcpConn, ws, "[ENTRY]")
	copyWSToTCP(ws, tcpConn, "[ENTRY]")

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

	ws.SetReadLimit(1 << 20)
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.SetPongHandler(func(string) error {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// 启动双向转发：WS->TCP 和 TCP->WS
	go copyWSToTCP(ws, tcpConn, "[EXIT]")
	copyTCPToWS(tcpConn, ws, "[EXIT]")

	log.Println("[EXIT] WS connection closed from", r.RemoteAddr)
}

///////////////////////
//  通用复制函数
///////////////////////

func copyTCPToWS(tcp net.Conn, ws *websocket.Conn, tag string) {
	buf := make([]byte, 4096)
	for {
		// 读玩家或MC服务器的TCP数据
		_ = tcp.SetReadDeadline(time.Now().Add(120 * time.Second))
		n, err := tcp.Read(buf)
		if err != nil {
			log.Println(tag, "TCP read error:", err)
			return
		}
		if n <= 0 {
			continue
		}

		// 写入到 WebSocket（二进制帧）
		_ = ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if err := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
			log.Println(tag, "WS write error:", err)
			return
		}
	}
}

func copyWSToTCP(ws *websocket.Conn, tcp net.Conn, tag string) {
	for {
		msgType, data, err := ws.ReadMessage()
		if err != nil {
			log.Println(tag, "WS read error:", err)
			return
		}
		if msgType != websocket.BinaryMessage {
			// 忽略文本帧等
			continue
		}

		_ = tcp.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if _, err := tcp.Write(data); err != nil {
			log.Println(tag, "TCP write error:", err)
			return
		}
	}
}
