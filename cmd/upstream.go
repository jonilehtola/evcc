package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/andig/wsp/client"
	"github.com/evcc-io/evcc/server"
	"github.com/evcc-io/evcc/util"
	"nhooyr.io/websocket"
)

func upstream(reverseProxyUrl, socketProxyUrl string, ch chan util.Param) {
	conf := client.NewConfig()
	conf.Targets = []string{reverseProxyUrl}
	client.NewClient(conf).Start(context.Background())

	for {
		if err := connectService(socketProxyUrl, ch); err != nil {
			time.Sleep(time.Second)
			fmt.Println("ws connect:", err)
		}
	}
}

func connectService(service string, ch <-chan util.Param) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, service, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()

	for p := range ch {
		msg := "{" + server.Kv(p) + "}"
		if err := conn.Write(context.Background(), websocket.MessageText, []byte(msg)); err != nil {
			return err
		}
	}

	return nil
}
