// Copyright 2025 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)

package network

import (
	"bytes"
	"fmt"
	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/ssh"
	"net"
	"strings"
	"testing"
	"time"
)

func TestConfigureSCC(t *testing.T) {
	username := "username"
	password := "password"
	upCommands := []string{
		"up_line1",
		"up line 2",
		"up-line/3",
		"up line 4",
		"up line 5",
		"exit",
	}
	downCommands := []string{
		"down_line1",
		"down line 2",
		"down-line/3",
		"down line 4",
		"down line 5",
		"exit",
	}
	scc := NewSCCSwitch("127.0.0.1", username, password, upCommands, downCommands)
	scc.port = 9150
	scc.connectTimeoutDuration = time.Second
	scc.configTimeoutDuration = time.Second

	var receivedUpCommands, receivedDownCommands []string

	// Set the switch to the down state
	mockSSHSwitch(t, scc.port, username, password, &receivedDownCommands)
	assert.Nil(t, scc.SetTeamEthernetEnabled(false))
	assert.Equal(t, downCommands, receivedDownCommands)
	assert.Equal(t, "DISABLED", scc.Status)

	// Set the switch to the up state
	scc.port += 1
	mockSSHSwitch(t, scc.port, username, password, &receivedUpCommands)
	assert.Nil(t, scc.SetTeamEthernetEnabled(true))
	assert.Equal(t, upCommands, receivedUpCommands)
	assert.Equal(t, "ACTIVE", scc.Status)
}

func mockSSHSwitch(t *testing.T, port int, username, password string, commands *[]string) {
	go func() {
		// Create a simple SSH server that accepts a connection with password authentication
		_, privateKey, err := ed25519.GenerateKey(nil)
		assert.Nil(t, err)
		signer, err := ssh.NewSignerFromKey(privateKey)
		assert.Nil(t, err)
		config := &ssh.ServerConfig{
			PasswordCallback: func(conn ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
				assert.Equal(t, username, conn.User())
				assert.Equal(t, password, string(pass))
				return nil, nil
			},
		}
		config.AddHostKey(signer)
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		assert.Nil(t, err)
		defer listener.Close()
		nConn, err := listener.Accept()
		assert.Nil(t, err)
		defer nConn.Close()
		conn, chans, reqs, err := ssh.NewServerConn(nConn, config)
		assert.Nil(t, err)
		defer conn.Close()

		// Ignore all client requests
		go ssh.DiscardRequests(reqs)

		// Wait for the client to connect and request a session
		rawChannel := <-chans
		assert.Equal(t, "session", rawChannel.ChannelType())
		channel, requests, err := rawChannel.Accept()
		assert.Nil(t, err)
		defer channel.Close()

		// Wait for the client to request a PTY
		req := <-requests
		assert.Equal(t, "pty-req", req.Type)
		req.Reply(true, nil)

		// Wait for the client to request a shell
		req = <-requests
		assert.Equal(t, "shell", req.Type)
		req.Reply(true, nil)

		// Read the data sent by the client until it sends the final "exit" command, as a real switch would. The client
		// never closes its stdin, so waiting for EOF or for a fixed amount of time is unreliable.
		received := make(chan string, 1)
		go func() {
			var receivedData bytes.Buffer
			buffer := make([]byte, 1024)
			for {
				n, err := channel.Read(buffer)
				receivedData.Write(buffer[:n])
				if err != nil || strings.HasSuffix(receivedData.String(), "exit\n") {
					break
				}
			}
			received <- receivedData.String()
		}()

		select {
		case data := <-received:
			*commands = strings.Split(data, "\n")
			if len(*commands) > 0 && (*commands)[len(*commands)-1] == "" {
				*commands = (*commands)[:len(*commands)-1] // Remove trailing newline
			}
		case <-time.After(time.Second):
			assert.Fail(t, "timed out waiting for the client to send its commands")
		}

		// Send an exit command to cleanly close the session
		channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
	}()
	time.Sleep(100 * time.Millisecond) // Give it some time to open the socket.
}
