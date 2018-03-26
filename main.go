package main

import (
	"bytes"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/astaxie/beego"
	"github.com/gorilla/websocket"
)

type BaseController struct {
	beego.Controller
}

// Create 创建房间
func (base *BaseController) Create() {
	rooname := base.GetString("roomname")
	fmt.Println("前端创建的房间:", rooname)
	room, err := CreateRoom(rand.Int63n(100000000), rooname)
	if err != nil {
		http.Error(base.Ctx.ResponseWriter, err.Error(), 400)
		return
	}
	go room.run()
	base.Data["json"] = room

	base.ServeJSON()
	base.StopRun()
	return
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// InRoom 进入房间
func (base *BaseController) InRoom() {
	room := new(Room)
	var (
		ok             bool
		name, roomname string
	)
	roomname = base.GetString("roomname")
	beego.Error("roomname:", roomname)
	name = base.GetString("name")
	beego.Error("name:", name)
	beego.Error("房间:", room)
	if name == "" {
		name = "hello world"
	}
	room, ok = Rooms[roomname]
	if !ok {
		room = Rooms["first"]
	}
	base.TplName = "home.html"
	conn, err := upgrader.Upgrade(base.Ctx.ResponseWriter, base.Ctx.Request, nil)
	if _, ok := err.(websocket.HandshakeError); ok {
		http.Error(base.Ctx.ResponseWriter, "不是合法的WebSocket请求", 400)
		return
	} else if err != nil {
		//beego.Error("设置WebSocket连接，失败：", err)
		return
	}

	// 创建一个客户端
	c := &Client{
		r:    room,
		UID:  1,
		Name: name,
		conn: conn,
		send: make(chan []byte, 256),
	}
	c.r.Register <- c //向聊天室注册客户端
	go c.readMessage()
	go c.WriteMessage()
}

func (base *BaseController) Romm() {
	base.TplName = "home.html"
}

func init() {
	Rooms = make(map[string]*Room, 0) //初始化放假列表
	//创建默认放假
	r, _ := CreateRoom(1, "first")
	Rooms["first"] = r
	go r.run()
	beego.Router("/", &BaseController{}, "GET:Romm")
	beego.Router("/ws", &BaseController{}, "GET:InRoom")
}
func main() {
	beego.Run("127.0.0.1:8080")
}

var (
	newline = []byte{'\n'}
	space   = []byte{' '}
)

const (
	writeWait      = 10 * time.Second //等待写消息超时设置
	pongWait       = 60 * time.Second //包活设置
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512
)

// Rooms 房间列表
var (
	Rooms map[string]*Room
)

// Room 房间
type Room struct {
	ID         int64  //房间ID
	Name       string //房间名字
	Clients    map[*Client]bool
	Broadcast  chan []byte  //需要广播的消息
	Register   chan *Client //刚进入到这个房间的客户端
	UnRegister chan *Client //退出到这个房间的客户端
}

// Client 客户端
type Client struct {
	r    *Room //属于哪个房间
	UID  int64
	Name string
	conn *websocket.Conn
	send chan []byte
}

// run 激活某个房间,这个房间可以进入 可以离开 并且推送消息
func (r *Room) run() {
	for {
		select {
		case c := <-r.Register: //如果有人进入房间,加入到客户端列表,并广播一条消息
			r.Clients[c] = true
			msg := fmt.Sprintf("欢迎 %s 进入房间!", c.Name)
			r.Broadcast <- []byte(msg)
		case unc := <-r.UnRegister: //如果有人离开房间,也广播一条消息
			delete(r.Clients, unc)
			close(unc.send) //关闭通道
			msg := fmt.Sprintf("%s 离开了放假!", unc.Name)
			r.Broadcast <- []byte(msg)
		case message := <-r.Broadcast: //当有消息的时候，发送消息
			for client := range r.Clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(r.Clients, client)
				}

			}

		}
	}
}

// CreateRoom 创建一个聊天室
func CreateRoom(roomID int64, roomName string) (*Room, error) {
	if _, ok := Rooms[roomName]; ok {
		return nil, fmt.Errorf("房间:%s 已经存在不能创建", roomName)
	}
	return &Room{
		ID:         roomID,
		Name:       roomName,
		Clients:    make(map[*Client]bool, 0),
		Broadcast:  make(chan []byte, 5),
		Register:   make(chan *Client, 0),
		UnRegister: make(chan *Client, 0),
	}, nil
}

// readMessage 客户端从websocket中读取消息并推送给其他客户端(我发的消息的时候)
func (c *Client) readMessage() {
	//如果客户端报错,那么就直接退出房间并且关闭客户端
	defer func() {
		c.r.UnRegister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)              //设置读取消息的最大限制
	c.conn.SetReadDeadline(time.Now().Add(pongWait)) //这种读超时

	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}
		//把换行符转换成' '
		message = bytes.TrimSpace(bytes.Replace(message, newline, space, -1))
		//然后把数据发送到hub的broadcast(这个是自己的hub)
		c.r.Broadcast <- message

	}
}

// WriteMessage 当我获取消息的时候 是我需要发送的列表中 写到我自己的客户端
func (c *Client) WriteMessage() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case mess, ok := <-c.send:
			//设置写超时
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok { //说明放假已经关闭
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(mess)
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write(newline)
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
