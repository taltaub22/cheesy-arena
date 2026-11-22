// Copyright 2026 Team 254. All Rights Reserved.
//
// Client-side methods for the team-facing E-stop and A-stop display.

var station = "";
var websocket;
var eStopped = false;
var aStopped = false;
var matchState = "";
var matchTimeSec = 0;
var realtimeScore = null;

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

// Updates the shift box from the latest realtime score: the time left in the current Hub shift, colored for the
// alliance whose Hub is active (or neutral when both are), and the winner of auto during the transition shift.
var updateShiftTimer = function () {
  var shift = "none";
  var remainingSec = 0;
  var caption = "SHIFT";
  if (realtimeScore && (matchState === "AUTO_PERIOD" || matchState === "TELEOP_PERIOD")) {
    var redRemainingSec = realtimeScore.Red.ActiveRemainingSec;
    var blueRemainingSec = realtimeScore.Blue.ActiveRemainingSec;
    remainingSec = Math.max(redRemainingSec, blueRemainingSec);
    if (redRemainingSec > 0 && blueRemainingSec > 0) {
      shift = "both";
    } else if (redRemainingSec > 0) {
      shift = "R";
    } else if (blueRemainingSec > 0) {
      shift = "B";
    }

    // The winner of auto is only decided once the teleop period starts, after the scoring grace period.
    var transitionEndSec = matchTiming.AutoDurationSec + matchTiming.PauseDurationSec +
      matchTiming.TransitionShiftDurationSec;
    if (matchState === "TELEOP_PERIOD" && matchTimeSec < transitionEndSec) {
      if (realtimeScore.Red.Score.Hub.WonAuto) {
        caption = "RED WON AUTO";
      } else if (realtimeScore.Blue.Score.Hub.WonAuto) {
        caption = "BLUE WON AUTO";
      }
    }
  }

  $("#shiftTimer").attr("data-shift", shift);
  $("#shiftTimerValue").text(shift === "none" ? "" : remainingSec);
  $("#shiftTimerCaption").text(caption).attr("data-winner", caption === "SHIFT" ? "" : caption[0]);
};

// Handles a websocket message to update the match state and timer.
var handleMatchTime = function (data) {
  matchState = matchStates[data.MatchState];
  matchTimeSec = data.MatchTimeSec;
  updateStopButtons();
  translateMatchTime(data, function (matchState, matchStateText, countdownSec) {
    $("#matchTimerValue").text(getCountdownString(Math.max(countdownSec, 0)));
  });
  updateShiftTimer();
};

// Handles a websocket message to update the realtime score, which carries the Hub shift timing and auto winner.
var handleRealtimeScore = function (data) {
  realtimeScore = data;
  updateShiftTimer();
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
    },
    realtimeScore: function (event) {
      handleRealtimeScore(event.data);
    }
  });
});
