// Copyright 2026 Team 254. All Rights Reserved.

package web

import (
	"github.com/Team254/cheesy-arena/field"
	"github.com/Team254/cheesy-arena/websocket"
	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestTeamEStopDisplay(t *testing.T) {
	web := setupTestWeb(t)

	recorder := web.getHttpResponse("/displays/team_estop")
	assert.Equal(t, 302, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Location"), "displayId=100")
	assert.Contains(t, recorder.Header().Get("Location"), "station=R1")

	recorder = web.getHttpResponse("/displays/team_estop?displayId=1&station=B1")
	assert.Equal(t, 200, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Team E-Stop Display - Untitled Event - Cheesy Arena")
	assert.Contains(t, recorder.Body.String(), "eStopButton")
	assert.Contains(t, recorder.Body.String(), "aStopButton")
}

func TestTeamEStopDisplayWebsocket(t *testing.T) {
	web := setupTestWeb(t)

	server, wsUrl := web.startTestServer()
	defer server.Close()
	conn, _, err := gorillawebsocket.DefaultDialer.Dial(
		wsUrl+"/displays/team_estop/websocket?displayId=1&station=B2", nil,
	)
	assert.Nil(t, err)
	defer conn.Close()
	ws := websocket.NewTestWebsocket(conn)

	// Should get a few status updates right after connection.
	readWebsocketType(t, ws, "displayConfiguration")
	readWebsocketType(t, ws, "matchTiming")
	readWebsocketType(t, ws, "arenaStatus")
	readWebsocketType(t, ws, "matchLoad")
	readWebsocketType(t, ws, "matchTime")
	readWebsocketType(t, ws, "realtimeScore")

	// Before the match, the E-stop is rejected and the A-stop can be made and released.
	ws.Write("eStop", nil)
	readUntilMessageType(t, ws, "error")
	assert.Equal(t, false, web.arena.AllianceStations["B2"].EStop)
	ws.Write("aStop", nil)
	readUntilMessageType(t, ws, "arenaStatus")
	assert.Equal(t, true, web.arena.AllianceStations["B2"].AStop)
	ws.Write("aStopRelease", nil)
	readUntilMessageType(t, ws, "arenaStatus")
	assert.Equal(t, false, web.arena.AllianceStations["B2"].AStop)

	// During the match, the display stops only its own station, and the A-stop can't be released.
	web.arena.MatchState = field.AutoPeriod
	ws.Write("aStop", nil)
	readUntilMessageType(t, ws, "arenaStatus")
	assert.Equal(t, true, web.arena.AllianceStations["B2"].AStop)
	ws.Write("aStopRelease", nil)
	readUntilMessageType(t, ws, "error")
	assert.Equal(t, true, web.arena.AllianceStations["B2"].AStop)
	ws.Write("eStop", nil)
	readUntilMessageType(t, ws, "arenaStatus")
	assert.Equal(t, true, web.arena.AllianceStations["B2"].EStop)
	for _, station := range []string{"R1", "R2", "R3", "B1", "B3"} {
		assert.Equal(t, false, web.arena.AllianceStations[station].EStop, station)
		assert.Equal(t, false, web.arena.AllianceStations[station].AStop, station)
	}

	// After the autonomous period, the A-stop is rejected.
	web.arena.MatchState = field.TeleopPeriod
	web.arena.AllianceStations["B2"].AStop = false
	ws.Write("aStop", nil)
	readUntilMessageType(t, ws, "error")
	assert.Equal(t, false, web.arena.AllianceStations["B2"].AStop)

	ws.Write("bogus", nil)
	readUntilMessageType(t, ws, "error")
}

// Reads websocket messages until one of the given type arrives, ignoring the initial status messages.
func readUntilMessageType(t *testing.T, ws *websocket.Websocket, expectedMessageType string) any {
	for i := 0; i < 20; i++ {
		messageType, message, err := ws.ReadWithTimeout(time.Second)
		if !assert.Nil(t, err) {
			return nil
		}
		if messageType == expectedMessageType {
			return message
		}
	}
	assert.Fail(t, "Did not receive message of type "+expectedMessageType)
	return nil
}
