// Copyright 2014 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)

package web

import (
	"github.com/Team254/cheesy-arena/field"
	"github.com/Team254/cheesy-arena/game"
	"github.com/Team254/cheesy-arena/websocket"
	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestAllianceStationDisplay(t *testing.T) {
	web := setupTestWeb(t)

	recorder := web.getHttpResponse("/displays/alliance_station")
	assert.Equal(t, 302, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Location"), "displayId=100")
	assert.Contains(t, recorder.Header().Get("Location"), "station=R1")

	recorder = web.getHttpResponse("/displays/alliance_station?displayId=1&station=B1")
	assert.Equal(t, 200, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "Alliance Station Display - Untitled Event - Cheesy Arena")
}

func TestAllianceStationDisplayWebsocket(t *testing.T) {
	web := setupTestWeb(t)

	server, wsUrl := web.startTestServer()
	defer server.Close()
	conn, _, err := gorillawebsocket.DefaultDialer.Dial(wsUrl+"/displays/alliance_station/websocket?displayId=1", nil)
	assert.Nil(t, err)
	defer conn.Close()
	ws := websocket.NewTestWebsocket(conn)

	// Should get a few status updates right after connection.
	readWebsocketType(t, ws, "displayConfiguration")
	readWebsocketType(t, ws, "matchTiming")
	readWebsocketType(t, ws, "allianceStationDisplayMode")
	readWebsocketType(t, ws, "arenaStatus")
	readWebsocketType(t, ws, "matchLoad")
	readWebsocketType(t, ws, "matchTime")
	readWebsocketType(t, ws, "realtimeScore")

	// Change to a different screen.
	web.arena.AllianceStationDisplayMode = "logo"
	web.arena.AllianceStationDisplayModeNotifier.Notify()
	readWebsocketType(t, ws, "allianceStationDisplayMode")

	// Run through a match cycle.
	web.arena.MatchLoadNotifier.Notify()
	readWebsocketType(t, ws, "matchLoad")
	web.arena.AllianceStations["R1"].Bypass = true
	web.arena.AllianceStations["R2"].Bypass = true
	web.arena.AllianceStations["R3"].Bypass = true
	web.arena.AllianceStations["B1"].Bypass = true
	web.arena.AllianceStations["B2"].Bypass = true
	web.arena.AllianceStations["B3"].Bypass = true
	web.arena.StartMatch()
	web.arena.Update()
	messages := readWebsocketMultiple(t, ws, 3)
	_, ok := messages["matchTime"]
	assert.True(t, ok)
	web.arena.MatchStartTime = time.Now().Add(-time.Duration(game.MatchTiming.AutoDurationSec) * time.Second)
	web.arena.Update()
	messages = readWebsocketMultiple(t, ws, 2)
	_, ok = messages["arenaStatus"]
	assert.True(t, ok)
	_, ok = messages["matchTime"]
	assert.True(t, ok)
	web.arena.RealtimeScoreNotifier.Notify()
	readWebsocketType(t, ws, "realtimeScore")
}

func TestAllianceStationDisplayStopButtons(t *testing.T) {
	web := setupTestWeb(t)

	recorder := web.getHttpResponse("/displays/alliance_station?displayId=1&station=B1")
	assert.Equal(t, 200, recorder.Code)
	recorder = web.getHttpResponse("/displays/alliance_station?displayId=1&station=B1&stops=true")
	assert.Equal(t, 200, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "eStopButton")

	server, wsUrl := web.startTestServer()
	defer server.Close()

	// A display without the stops option must not be able to stop anything.
	conn, _, err := gorillawebsocket.DefaultDialer.Dial(
		wsUrl+"/displays/alliance_station/websocket?displayId=1&station=R1", nil,
	)
	assert.Nil(t, err)
	defer conn.Close()
	ws := websocket.NewTestWebsocket(conn)
	web.arena.MatchState = field.TeleopPeriod
	ws.Write("eStop", nil)
	readUntilMessageType(t, ws, "error")
	assert.Equal(t, false, web.arena.AllianceStations["R1"].EStop)

	// A display with the stops option stops only its own station.
	conn2, _, err := gorillawebsocket.DefaultDialer.Dial(
		wsUrl+"/displays/alliance_station/websocket?displayId=2&station=B2&stops=true", nil,
	)
	assert.Nil(t, err)
	defer conn2.Close()
	ws2 := websocket.NewTestWebsocket(conn2)
	ws2.Write("eStop", nil)
	readUntilMessageType(t, ws2, "arenaStatus")
	assert.Equal(t, true, web.arena.AllianceStations["B2"].EStop)
	assert.Equal(t, false, web.arena.AllianceStations["B1"].EStop)
	assert.Equal(t, false, web.arena.AllianceStations["R1"].EStop)

	// A-stops are only accepted during the autonomous period.
	ws2.Write("aStop", nil)
	readUntilMessageType(t, ws2, "error")
	assert.Equal(t, false, web.arena.AllianceStations["B2"].AStop)
	web.arena.MatchState = field.AutoPeriod
	ws2.Write("aStop", nil)
	readUntilMessageType(t, ws2, "arenaStatus")
	assert.Equal(t, true, web.arena.AllianceStations["B2"].AStop)
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
