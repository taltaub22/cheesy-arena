// Copyright 2026 Team 254. All Rights Reserved.
//
// Client-side methods for the team-facing E-stop and A-stop display.

var station = "";
var websocket;
var eStopped = false;
var aStopped = false;
var matchState = "";

// Updates the stop buttons from the match state and the latched stops. The E-stop is usable only while a match is in
// progress. The A-stop is usable before the match and during the autonomous period; an A-stop made before the match can
// be released by tapping the button again until the match starts.
var updateStopButtons = function () {
  var eStopAvailable = ["START_MATCH", "AUTO_PERIOD", "PAUSE_PERIOD", "TELEOP_PERIOD"].includes(matchState);
  $("#eStopButton")
    .prop("disabled", eStopped || !eStopAvailable)
    .attr("data-latched", eStopped)
    .attr("data-unavailable", !eStopped && !eStopAvailable)
    .text(eStopped ? "E-STOPPED" : "E-STOP");

  var aStopAvailable = ["PRE_MATCH", "START_MATCH", "AUTO_PERIOD"].includes(matchState);
  var aStopReleasable = aStopped && matchState === "PRE_MATCH";
  $("#aStopButton")
    .prop("disabled", aStopped ? !aStopReleasable : !aStopAvailable)
    .attr("data-latched", aStopped)
    .attr("data-unavailable", !aStopped && !aStopAvailable)
    .html(aStopped ? "A-STOPPED" + (aStopReleasable ? "<small>Tap to release</small>" : "") : "A-STOP");
};

// Handles a websocket message to update the team to display.
var handleMatchLoad = function (data) {
  var team = data.Teams[station];
  $("#teamNumber").text(team ? team.Id : "");
};

// Handles a websocket message to update the latched stops for this station.
var handleArenaStatus = function (data) {
  var stationStatus = data.AllianceStations[station];
  if (stationStatus) {
    eStopped = stationStatus.EStop;
    aStopped = stationStatus.AStop;
    updateStopButtons();
  }
};

// Handles a websocket message to update the match state.
var handleMatchTime = function (data) {
  matchState = matchStates[data.MatchState];
  updateStopButtons();
};

$(function () {
  // Read the configuration for this display from the URL query string.
  var urlParams = new URLSearchParams(window.location.search);
  station = urlParams.get("station");
  $("body").attr("data-alliance", station[0]);
  updateStopButtons();

  $("#eStopButton").on("click", function () {
    websocket.send("eStop", null);
  });
  $("#aStopButton").on("click", function () {
    websocket.send(aStopped ? "aStopRelease" : "aStop", null);
  });

  // Set up the websocket back to the server.
  websocket = new CheesyWebsocket("/displays/team_estop/websocket", {
    arenaStatus: function (event) {
      handleArenaStatus(event.data);
    },
    matchLoad: function (event) {
      handleMatchLoad(event.data);
    },
    matchTime: function (event) {
      handleMatchTime(event.data);
    },
    matchTiming: function (event) {
      handleMatchTiming(event.data);
    }
  });
});
