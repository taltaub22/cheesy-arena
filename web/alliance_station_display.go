// Copyright 2014 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Web handlers for the alliance station display.

package web

import (
	"fmt"
	"github.com/Team254/cheesy-arena/model"
	"github.com/Team254/cheesy-arena/websocket"
	"io"
	"log"
	"net/http"
)

// Renders the team number and status display shown above each alliance station. If the "stops" option is enabled, the
// display also shows touch buttons that let the team E-stop or A-stop its own station.
func (web *Web) allianceStationDisplayHandler(w http.ResponseWriter, r *http.Request) {
	if !web.enforceDisplayConfiguration(w, r, map[string]string{"station": "R1", "stops": "false"}) {
		return
	}

	template, err := web.parseFiles("templates/alliance_station_display.html")
	if err != nil {
		handleWebErr(w, err)
		return
	}

	data := struct {
		*model.EventSettings
	}{web.arena.EventSettings}
	err = template.ExecuteTemplate(w, "alliance_station_display.html", data)
	if err != nil {
		handleWebErr(w, err)
		return
	}
}

// The websocket endpoint for the alliance station display client to receive status updates.
func (web *Web) allianceStationDisplayWebsocketHandler(w http.ResponseWriter, r *http.Request) {
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
		web.arena.AllianceStationDisplayModeNotifier,
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

		switch messageType {
		case "eStop", "aStop":
			if display.DisplayConfiguration.Configuration["stops"] != "true" {
				writeWebsocketError(ws, "Stop buttons are not enabled for this display.")
				continue
			}
			station := display.DisplayConfiguration.Configuration["station"]
			if messageType == "eStop" {
				err = web.arena.EStop(station)
			} else {
				err = web.arena.AStop(station)
			}
			if err != nil {
				writeWebsocketError(ws, err.Error())
			}
		default:
			writeWebsocketError(ws, fmt.Sprintf("Invalid message type '%s'.", messageType))
		}
	}
}
