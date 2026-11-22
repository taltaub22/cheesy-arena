// Copyright 2026 Team 254. All Rights Reserved.

package web

import (
	"github.com/Team254/cheesy-arena/field"
	"github.com/Team254/cheesy-arena/websocket"
	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"testing"
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
	assert.Contains(t, recorder.Body.String(), `data-station-valid="true"`)
	assert.NotContains(t, recorder.Body.String(), "INVALID STATION")

	// A display set up for a non-team station still gets the page, but marked as unusable.
	for _, station := range []string{"N1", ""} {
		recorder = web.getHttpResponse("/displays/team_estop?displayId=1&station=" + station)
		assert.Equal(t, 200, recorder.Code, station)
		assert.Contains(t, recorder.Body.String(), `data-station-valid="false"`, station)
		assert.Contains(t, recorder.Body.String(), "INVALID STATION", station)
	}
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
	readWebsocketTypeEventually(t, ws, "error", 20)
	assert.Equal(t, false, web.arena.AllianceStations["B2"].EStop)
	ws.Write("aStop", nil)
	readWebsocketTypeEventually(t, ws, "arenaStatus", 20)
	assert.Equal(t, true, web.arena.AllianceStations["B2"].AStop)
	ws.Write("aStopRelease", nil)
	readWebsocketTypeEventually(t, ws, "arenaStatus", 20)
	assert.Equal(t, false, web.arena.AllianceStations["B2"].AStop)

	// During the match, the display stops only its own station, and the A-stop can't be released.
	web.arena.MatchState = field.AutoPeriod
	ws.Write("aStop", nil)
	readWebsocketTypeEventually(t, ws, "arenaStatus", 20)
	assert.Equal(t, true, web.arena.AllianceStations["B2"].AStop)
	ws.Write("aStopRelease", nil)
	readWebsocketTypeEventually(t, ws, "error", 20)
	assert.Equal(t, true, web.arena.AllianceStations["B2"].AStop)
	ws.Write("eStop", nil)
	readWebsocketTypeEventually(t, ws, "arenaStatus", 20)
	assert.Equal(t, true, web.arena.AllianceStations["B2"].EStop)
	for _, station := range []string{"R1", "R2", "R3", "B1", "B3"} {
		assert.Equal(t, false, web.arena.AllianceStations[station].EStop, station)
		assert.Equal(t, false, web.arena.AllianceStations[station].AStop, station)
	}

	// After the autonomous period, the A-stop is rejected.
	web.arena.MatchState = field.TeleopPeriod
	web.arena.AllianceStations["B2"].AStop = false
	ws.Write("aStop", nil)
	readWebsocketTypeEventually(t, ws, "error", 20)
	assert.Equal(t, false, web.arena.AllianceStations["B2"].AStop)

	ws.Write("bogus", nil)
	readWebsocketTypeEventually(t, ws, "error", 20)
}

func TestTeamEStopDisplayWebsocketTimeout(t *testing.T) {
	web := setupTestWeb(t)

	server, wsUrl := web.startTestServer()
	defer server.Close()
	conn, _, err := gorillawebsocket.DefaultDialer.Dial(
		wsUrl+"/displays/team_estop/websocket?displayId=1&station=R3", nil,
	)
	assert.Nil(t, err)
	defer conn.Close()
	ws := websocket.NewTestWebsocket(conn)
	readWebsocketTypeEventually(t, ws, "realtimeScore", 10)

	// During a timeout, the A-stop can be made and released again.
	for _, matchState := range []field.MatchState{field.TimeoutActive, field.PostTimeout} {
		web.arena.MatchState = matchState
		ws.Write("aStop", nil)
		readWebsocketTypeEventually(t, ws, "arenaStatus", 20)
		assert.Equal(t, true, web.arena.AllianceStations["R3"].AStop, matchState)
		assert.Equal(t, true, web.arena.AllianceStations["R3"].PreMatchAStop, matchState)
		ws.Write("aStopRelease", nil)
		readWebsocketTypeEventually(t, ws, "arenaStatus", 20)
		assert.Equal(t, false, web.arena.AllianceStations["R3"].AStop, matchState)
		assert.Equal(t, false, web.arena.AllianceStations["R3"].PreMatchAStop, matchState)
	}
}

func TestTeamEStopDisplayWebsocketInvalidStation(t *testing.T) {
	web := setupTestWeb(t)

	server, wsUrl := web.startTestServer()
	defer server.Close()
	conn, _, err := gorillawebsocket.DefaultDialer.Dial(
		wsUrl+"/displays/team_estop/websocket?displayId=1&station=N1", nil,
	)
	assert.Nil(t, err)
	defer conn.Close()
	ws := websocket.NewTestWebsocket(conn)

	// The display still connects, so it can be reconfigured, but every stop command is rejected.
	readWebsocketType(t, ws, "displayConfiguration")
	readWebsocketTypeEventually(t, ws, "realtimeScore", 10)
	web.arena.MatchState = field.AutoPeriod
	for _, messageType := range []string{"eStop", "aStop", "aStopRelease"} {
		ws.Write(messageType, nil)
		message := readWebsocketTypeEventually(t, ws, "error", 20)
		assert.Contains(t, message, "not configured for a valid alliance station", messageType)
	}
	for station, allianceStation := range web.arena.AllianceStations {
		assert.Equal(t, false, allianceStation.EStop, station)
		assert.Equal(t, false, allianceStation.AStop, station)
	}
}

func TestTeamEStopDisplayWebsocketStationReadOnce(t *testing.T) {
	web := setupTestWeb(t)

	server, wsUrl := web.startTestServer()
	defer server.Close()
	conn, _, err := gorillawebsocket.DefaultDialer.Dial(
		wsUrl+"/displays/team_estop/websocket?displayId=1&station=B1", nil,
	)
	assert.Nil(t, err)
	defer conn.Close()
	ws := websocket.NewTestWebsocket(conn)
	readWebsocketTypeEventually(t, ws, "realtimeScore", 10)

	// A second display registering with the same ID but a different station replaces the shared configuration, but
	// must not change which station the first display's connection stops.
	conn2, _, err := gorillawebsocket.DefaultDialer.Dial(
		wsUrl+"/displays/team_estop/websocket?displayId=1&station=R2", nil,
	)
	assert.Nil(t, err)
	defer conn2.Close()
	ws2 := websocket.NewTestWebsocket(conn2)
	readWebsocketTypeEventually(t, ws2, "realtimeScore", 10)
	assert.Equal(t, "R2", web.arena.Displays["1"].DisplayConfiguration.Configuration["station"])

	web.arena.MatchState = field.AutoPeriod
	ws.Write("eStop", nil)
	readWebsocketTypeEventually(t, ws, "arenaStatus", 20)
	assert.Equal(t, true, web.arena.AllianceStations["B1"].EStop)
	assert.Equal(t, false, web.arena.AllianceStations["R2"].EStop)
}
