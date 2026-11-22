// Copyright 2026 Team 254. All Rights Reserved.
//
// Client-side methods for the team-facing E-stop and A-stop display.

// The server sends arenaStatus at least every 500 ms (dsPacketPeriodMs) in every match state, so a longer gap means the
// display is no longer hearing from the field and its button presses may not be getting through.
const arenaStatusTimeoutMs = 1500;
// How long A-STOP taps are ignored after a tap is sent or the A-stop latch changes, so a double-tap can't undo itself.
const aStopDebounceMs = 1000;
// How long a server error message stays on screen.
const errorMessageDurationMs = 3000;

var station = "";
var stationValid = false;
var websocket;
var websocketOpen = false;
var lastArenaStatusTime = 0;
var connected = false;
var eStopped = false;
var aStopped = false;
var preMatchAStop = false;
var aStopIgnoreUntil = 0;
var matchState = "";
var matchTimeSec = 0;
var realtimeScore = null;
var errorMessageTimeout;

// Returns true if the A-stop is latched and can be released from this display: only one made in software before the
// match (not a physical button being held), and only while the match hasn't started.
var isAStopReleasable = function () {
  return aStopped && preMatchAStop && ["PRE_MATCH", "TIMEOUT_ACTIVE", "POST_TIMEOUT"].includes(matchState);
};

// Updates the stop buttons from the match state and the latched stops. The E-stop is usable only while a match is in
// progress. The A-stop is usable before the match and during the autonomous period; an A-stop made before the match can
// be released by tapping the button again until the match starts. Both are disabled while the display is disconnected.
var updateStopButtons = function () {
  var usable = connected && stationValid;
  var eStopAvailable = ["START_MATCH", "AUTO_PERIOD", "PAUSE_PERIOD", "TELEOP_PERIOD"].includes(matchState);
  $("#eStopButton")
    .prop("disabled", !usable || eStopped || !eStopAvailable)
    .attr("data-latched", eStopped)
    .attr("data-unavailable", !eStopped && !eStopAvailable)
    .text(eStopped ? "E-STOPPED" : "E-STOP");

  var aStopAvailable = ["PRE_MATCH", "START_MATCH", "AUTO_PERIOD", "TIMEOUT_ACTIVE", "POST_TIMEOUT"].includes(
    matchState
  );
  var aStopReleasable = isAStopReleasable();
  $("#aStopButton")
    .prop("disabled", !usable || (aStopped ? !aStopReleasable : !aStopAvailable))
    .attr("data-latched", aStopped)
    .attr("data-unavailable", !aStopped && !aStopAvailable)
    .html(aStopped ? "A-STOPPED" + (aStopReleasable ? "<small>Tap to release</small>" : "") : "A-STOP");
};

// Re-evaluates whether the display is connected: the websocket must be open and an arenaStatus must have arrived
// recently. Greys out the buttons and shows a full-width banner whenever it isn't.
var updateConnection = function () {
  var nowConnected = websocketOpen && Date.now() - lastArenaStatusTime < arenaStatusTimeoutMs;
  if (nowConnected === connected) {
    return;
  }
  connected = nowConnected;
  $("body").attr("data-connected", connected);
  $("#disconnectedBanner").toggle(!connected);
  updateStopButtons();
};

// Shows a server error briefly without blocking the page, so that the buttons keep working.
var showErrorMessage = function (message) {
  console.log(message);
  $("#errorMessage").text(message).show();
  clearTimeout(errorMessageTimeout);
  errorMessageTimeout = setTimeout(function () {
    $("#errorMessage").hide();
  }, errorMessageDurationMs);
};

// Sends a command to the server, if connected.
var sendCommand = function (type) {
  if (!connected) {
    return;
  }
  try {
    websocket.send(type, null);
  } catch (err) {
    showErrorMessage("Could not send command: " + err);
  }
};

// Handles a websocket message to update the team to display.
var handleMatchLoad = function (data) {
  var team = data.Teams[station];
  $("#teamNumber").text(team ? team.Id : "");
};

// Handles a websocket message to update the latched stops for this station.
var handleArenaStatus = function (data) {
  var stationStatus = data.AllianceStations[station];
  lastArenaStatusTime = Date.now();
  if (stationStatus) {
    if (stationStatus.AStop !== aStopped) {
      aStopIgnoreUntil = Date.now() + aStopDebounceMs;
    }
    eStopped = stationStatus.EStop;
    aStopped = stationStatus.AStop;
    preMatchAStop = stationStatus.PreMatchAStop === true;
  }
  updateStopButtons();
  updateConnection();
};

// Updates the shift box from the latest realtime score: the time left in the current Hub shift, colored for the
// alliance whose Hub is active (or neutral when both are), and the winner of auto during the transition shift (when both
// Hubs are active), so that the neutral color and the caption change together.
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
    if (matchState === "TELEOP_PERIOD" && shift === "both" && matchTimeSec < transitionEndSec) {
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
  // Read the configuration for this display from the URL query string. The server marks whether it's a valid station.
  var urlParams = new URLSearchParams(window.location.search);
  station = urlParams.get("station") || "";
  stationValid = $("body").attr("data-station-valid") === "true";
  $("body").attr("data-alliance", station.charAt(0)).attr("data-connected", connected);
  $("#disconnectedBanner").show();
  updateStopButtons();

  // The E-stop is never debounced; every tap is sent straight away.
  $("#eStopButton").on("click", function () {
    sendCommand("eStop");
  });
  $("#aStopButton").on("click", function () {
    if (Date.now() < aStopIgnoreUntil) {
      return;
    }
    aStopIgnoreUntil = Date.now() + aStopDebounceMs;
    if (isAStopReleasable()) {
      sendCommand("aStopRelease");
    } else if (!aStopped) {
      sendCommand("aStop");
    }
  });

  // Set up the websocket back to the server.
  websocket = new CheesyWebsocket(
    "/displays/team_estop/websocket",
    {
      arenaStatus: function (event) {
        handleArenaStatus(event.data);
      },
      error: function (event) {
        showErrorMessage(event.data);
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
    },
    {
      open: function () {
        // Stay disconnected until the first arenaStatus arrives, which the server sends right after connecting.
        websocketOpen = true;
        updateConnection();
      },
      close: function () {
        websocketOpen = false;
        updateConnection();
      }
    }
  );

  // Watch for arenaStatus messages stopping even though the websocket still looks open.
  setInterval(updateConnection, 250);
});
