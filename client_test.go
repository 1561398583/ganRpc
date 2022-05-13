package GanRPC

import (
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"testing"
)

func TestClient(t *testing.T) {
	opt := DefaultOption
	//连接一个不回应的server，测试dial timeout
	go startBadServer()
	client, err := Dial("tcp", ":9001", opt)
	if err.Error() != "create client time out"{
		t.Fatal("exception create client time out , but : " + err.Error())
	}

	//连接正常的server
	go startServer()
	client, err = Dial("tcp", ":9000", opt)
	if err != nil{
		t.Fatal("dial server error " + err.Error())
	}

	arg := Args{1, 2}
	var reply int
	err = client.Call("Foo.Sum", arg, &reply)
	if err != nil{
		t.Fatal("call Foo.Sum error " + err.Error())
	}
	if reply != 3 {
		t.Error("expect 3 but " + strconv.FormatInt(int64(reply), 10))
	}

	//测试call timeout
	err = client.Call("Foo.DoTimeout", arg, &reply)
	if err.Error() != "timeout" {
		t.Error("expect timeout but not")
	}

	//测试并发call
	wg := new(sync.WaitGroup)
	for i := 0; i < 100; i++{
		wg.Add(1)
		calls(client, t, i, wg)
	}
	wg.Wait()
}

func startBadServer()  {
	defer func() {
		fmt.Println("bad server closed")
	}()
	l, err := net.Listen("tcp", ":9001")
	if err != nil {
		log.Fatal(err)
	}
	conn, err := l.Accept()
	if err != nil {
		log.Fatal(err)
	}
	for {
		bs := make([]byte, 1)
		conn.Read(bs)
	}
}


func startServer()  {
	var foo Foo
	err := RegisterService(foo) //注册服务到DefaultServer中
	if err != nil {
		log.Fatal(err)
	}
	l, err := net.Listen("tcp", ":9000")
	if err != nil {
		log.Fatal(err)
	}

	log.Println("start rpc server on "+l.Addr().String())
	Accept(l)
}

func calls(client *Client, t *testing.T, i int, wg *sync.WaitGroup)  {
	defer wg.Done()
	arg := Args{i, i + 1}
	var reply int
	err := client.Call("Foo.Sum", arg, &reply)
	if err != nil{
		t.Error("call Foo.Sum error " + err.Error())
	}
	if reply != i * 2 + 1 {
		t.Error("expect  " + strconv.FormatInt(int64(i * 2 + 1), 10) + " but " + strconv.FormatInt(int64(reply), 10))
	}
}


