// Copyright 2026 Team 254. All Rights Reserved.
//
// Web routes for the team-facing E-stop and A-stop display.

package web

import (
	"fmt"
	"github.com/Team254/cheesy-arena/model"
	"github.com/Team254/cheesy-arena/websocket"
	"io"
	"log"
	"net/http"
)

// Renders the team-facing display at each alliance station, showing the team number and touch buttons that let the
// team E-stop or A-stop its own robot.
func (web *Web) teamEStopDisplayHandler(w http.ResponseWriter, r *http.Request) {
	if !web.enforceDisplayConfiguration(w, r, map[string]string{"station": "R1"}) {
		return
	}

	template, err := web.parseFiles("templates/team_estop_display.html")
	if err != nil {
		handleWebErr(w, err)
		return
	}

	data := struct {
		*model.EventSettings
	}{web.arena.EventSettings}
	err = template.ExecuteTemplate(w, "team_estop_display.html", data)
	if err != nil {
		handleWebErr(w, err)
		return
	}
}

// The websocket endpoint for the team E-stop display client to receive status updates and send stop commands.
func (web *Web) teamEStopDisplayWebsocketHandler(w http.ResponseWriter, r *http.Request) {
	display, err := web.registerDisplay(r)
	if err != nil {
		handleWebErr(w, err)
		return
	}
	defer web.arena.MarkDisplayDisconnected(display.DisplayConfiguration.Id)

	ws, err := websocket.NewWebsocket(w, r)
	if err != nil {
		handleWebErr(w, err)
		return
	}
	defer closeWebsocket(ws)

	// Subscribe the websocket to the notifiers whose messages will be passed on to the client, in a separate goroutine.
	go ws.HandleNotifiers(
		display.Notifier,
		web.arena.MatchTimingNotifier,
		web.arena.ArenaStatusNotifier,
		web.arena.MatchLoadNotifier,
		web.arena.MatchTimeNotifier,
		web.arena.RealtimeScoreNotifier,
		web.arena.ReloadDisplaysNotifier,
	)

	// Loop, waiting for stop commands and responding to them, until the client closes the connection. The station is
	// always taken from the display's own configuration, so a display can only ever stop its own station.
	for {
		messageType, _, err := ws.Read()
		if err != nil {
			if err == io.EOF {
				// Client has closed the connection; nothing to do here.
				return
			}
			log.Println(err)
			return
		}

		station := display.DisplayConfiguration.Configuration["station"]
		switch messageType {
		case "eStop":
			err = web.arena.EStop(station)
		case "aStop":
			err = web.arena.AStop(station)
		case "aStopRelease":
			err = web.arena.ReleaseAStop(station)
		default:
			err = fmt.Errorf("Invalid message type '%s'.", messageType)
		}
		if err != nil {
			writeWebsocketError(ws, err.Error())
		}
	}
}
