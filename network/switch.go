// Copyright 2014 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)
//
// Methods for configuring team VLANs and DHCP on a UniFi gateway (UniFi OS), in place of a Cisco Catalyst switch.

package network

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/Team254/cheesy-arena/model"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	switchConfigBackoffDurationSec = 5
	switchHttpTimeoutSec           = 15
	switchTeamGatewayAddress       = 4
	switchDhcpRangeStart           = 20
	switchDhcpRangeEnd             = 199
	switchUniFiSite                = "default"
)

const (
	red1Vlan  = 10
	red2Vlan  = 20
	red3Vlan  = 30
	blue1Vlan = 40
	blue2Vlan = 50
	blue3Vlan = 60
)

// The VLAN of each alliance station, in the same order as the teams passed to ConfigureTeamEthernet.
var stationVlans = [6]int{red1Vlan, red2Vlan, red3Vlan, blue1Vlan, blue2Vlan, blue3Vlan}

// Configures the six alliance station networks on a UniFi gateway. Despite the historical name, this no longer talks to
// a Cisco switch; the address is the gateway's URL and the API key is one created in the UniFi Network application.
type Switch struct {
	address               string
	apiKey                string
	mutex                 sync.Mutex
	configBackoffDuration time.Duration
	Status                string
}

const ServerIpAddress = "10.0.100.5" // The DS will try to connect to this address only.

// DevMode allows driver station listeners to bind to all local IP addresses.
var DevMode = false

// A UniFi network as returned by the controller. The raw fields are kept so that an update sends back everything that
// was received, with only the fields we manage changed.
type uniFiNetwork struct {
	raw map[string]any
}

// The desired state of one alliance station network.
type uniFiNetworkTarget struct {
	subnet      string // CIDR notation of the gateway address, e.g. "10.2.54.4/24".
	dhcpEnabled bool
	dhcpStart   string
	dhcpStop    string
}

// A client for the UniFi OS gateway that authenticates each request with an API key.
type uniFiSession struct {
	baseUrl string
	apiKey  string
	client  *http.Client
}

func NewSwitch(address, apiKey string) *Switch {
	return &Switch{
		address:               address,
		apiKey:                apiKey,
		configBackoffDuration: switchConfigBackoffDurationSec * time.Second,
		Status:                "UNKNOWN",
	}
}

// Sets up wired networks for the given set of teams.
func (sw *Switch) ConfigureTeamEthernet(teams [6]*model.Team) error {
	// Make sure multiple configurations aren't being set at the same time.
	sw.mutex.Lock()
	defer sw.mutex.Unlock()
	sw.Status = "CONFIGURING"

	changed, err := sw.configure(teams)
	if err != nil {
		sw.Status = "ERROR"
		return err
	}

	// Give some time for the configuration to take before another one can be attempted.
	if changed {
		time.Sleep(sw.configBackoffDuration)
	}

	sw.Status = "ACTIVE"
	return nil
}

// Computes the desired state of each station network, connects to the gateway and applies any differences. Returns
// whether anything was changed.
func (sw *Switch) configure(teams [6]*model.Team) (bool, error) {
	// Fail early if the same team is in two stations, since that would require two networks with the same subnet.
	seenTeams := make(map[int]bool)
	for _, team := range teams {
		if team == nil {
			continue
		}
		if seenTeams[team.Id] {
			return false, fmt.Errorf("team %d is assigned to more than one alliance station", team.Id)
		}
		seenTeams[team.Id] = true
	}

	targets := make(map[int]uniFiNetworkTarget)
	for i, vlan := range stationVlans {
		targets[vlan] = teamNetworkTarget(teams[i], vlan)
	}

	session, err := sw.newSession()
	if err != nil {
		return false, err
	}
	networks, err := session.listNetworks()
	if err != nil {
		return false, err
	}

	// Find the network for each station VLAN.
	stationNetworks := make(map[int]*uniFiNetwork)
	for _, vlan := range stationVlans {
		for _, network := range networks {
			if networkVlan, ok := network.vlan(); ok && networkVlan == vlan {
				if stationNetworks[vlan] != nil {
					return false, fmt.Errorf("more than one UniFi network has VLAN ID %d", vlan)
				}
				stationNetworks[vlan] = network
			}
		}
		if stationNetworks[vlan] == nil {
			return false, fmt.Errorf("no UniFi network with VLAN ID %d was found", vlan)
		}
	}

	// Work out which networks need to change.
	pending := make(map[int]bool)
	for _, vlan := range stationVlans {
		if !stationNetworks[vlan].matches(targets[vlan]) {
			pending[vlan] = true
		}
	}
	if len(pending) == 0 {
		return false, nil
	}

	// UniFi refuses to save a network whose subnet overlaps another one, so apply the changes in an order that never
	// creates an overlap. If the changes form a cycle (e.g. two teams swapping stations), park one network on its
	// placeholder subnet to break it.
	for len(pending) > 0 {
		progressed := false
		for _, vlan := range stationVlans {
			if !pending[vlan] {
				continue
			}
			if overlapsAnother(targets[vlan].subnet, vlan, stationNetworks) {
				continue
			}
			if err = session.updateNetwork(stationNetworks[vlan], targets[vlan]); err != nil {
				return true, err
			}
			delete(pending, vlan)
			progressed = true
		}
		if progressed {
			continue
		}

		parked := false
		for _, vlan := range stationVlans {
			if !pending[vlan] {
				continue
			}
			placeholder := teamNetworkTarget(nil, vlan)
			if stationNetworks[vlan].matches(placeholder) {
				continue
			}
			if err = session.updateNetwork(stationNetworks[vlan], placeholder); err != nil {
				return true, err
			}
			parked = true
			break
		}
		if !parked {
			return true, fmt.Errorf("unable to find a valid order in which to update the UniFi networks")
		}
	}

	return true, nil
}

// Returns the desired network state for the given team in the given station VLAN. A station without a team gets a
// placeholder subnet that is unique to the VLAN, with DHCP turned off.
func teamNetworkTarget(team *model.Team, vlan int) uniFiNetworkTarget {
	if team == nil {
		return uniFiNetworkTarget{subnet: fmt.Sprintf("10.255.%d.%d/24", vlan, switchTeamGatewayAddress)}
	}
	prefix := fmt.Sprintf("10.%d.%d", team.Id/100, team.Id%100)
	return uniFiNetworkTarget{
		subnet:      fmt.Sprintf("%s.%d/24", prefix, switchTeamGatewayAddress),
		dhcpEnabled: true,
		dhcpStart:   fmt.Sprintf("%s.%d", prefix, switchDhcpRangeStart),
		dhcpStop:    fmt.Sprintf("%s.%d", prefix, switchDhcpRangeEnd),
	}
}

// Returns true if the given subnet would overlap the current subnet of any station network other than the given one.
func overlapsAnother(subnet string, vlan int, stationNetworks map[int]*uniFiNetwork) bool {
	_, target, err := net.ParseCIDR(subnet)
	if err != nil {
		return false
	}
	for otherVlan, network := range stationNetworks {
		if otherVlan == vlan {
			continue
		}
		_, current, err := net.ParseCIDR(network.stringField("ip_subnet"))
		if err != nil {
			continue
		}
		if target.Contains(current.IP) || current.Contains(target.IP) {
			return true
		}
	}
	return false
}

// Creates a client for the gateway using the configured API key.
func (sw *Switch) newSession() (*uniFiSession, error) {
	if sw.apiKey == "" {
		return nil, fmt.Errorf("the switch API key is not set; create one in the UniFi Network application")
	}
	baseUrl := strings.TrimRight(sw.address, "/")
	if baseUrl == "" {
		return nil, fmt.Errorf("the switch address is not set; enter the UniFi gateway URL")
	}
	if !strings.Contains(baseUrl, "://") {
		baseUrl = "https://" + baseUrl
	}

	return &uniFiSession{
		baseUrl: baseUrl,
		apiKey:  sw.apiKey,
		client: &http.Client{
			Timeout: switchHttpTimeoutSec * time.Second,
			// The gateway uses a self-signed certificate by default, and it is only reachable on the local network.
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
	}, nil
}

// Sends a request to the gateway and returns the response body. Fails on any status other than 200.
func (session *uniFiSession) request(method, path string, body []byte) ([]byte, error) {
	request, err := http.NewRequest(method, session.baseUrl+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-API-KEY", session.apiKey)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := session.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 200 {
			message = message[:200]
		}
		return nil, fmt.Errorf("%s %s returned status %d: %s", method, path, response.StatusCode, message)
	}
	return responseBody, nil
}

// Unwraps the standard UniFi Network API response envelope and returns its data array.
func parseUniFiData(responseBody []byte) ([]map[string]any, error) {
	var envelope struct {
		Meta struct {
			Rc  string `json:"rc"`
			Msg string `json:"msg"`
		} `json:"meta"`
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return nil, fmt.Errorf("unable to parse the UniFi response: %v", err)
	}
	if envelope.Meta.Rc != "ok" {
		return nil, fmt.Errorf("UniFi returned an error: %s", envelope.Meta.Msg)
	}
	return envelope.Data, nil
}

func networkConfPath(id string) string {
	path := fmt.Sprintf("/proxy/network/api/s/%s/rest/networkconf", switchUniFiSite)
	if id != "" {
		path += "/" + id
	}
	return path
}

// Fetches all the networks configured on the gateway.
func (session *uniFiSession) listNetworks() ([]*uniFiNetwork, error) {
	responseBody, err := session.request("GET", networkConfPath(""), nil)
	if err != nil {
		return nil, err
	}
	data, err := parseUniFiData(responseBody)
	if err != nil {
		return nil, err
	}
	networks := make([]*uniFiNetwork, len(data))
	for i, raw := range data {
		networks[i] = &uniFiNetwork{raw: raw}
	}
	return networks, nil
}

// Applies the given target to the given network, and records the change locally if the gateway accepts it.
func (session *uniFiSession) updateNetwork(network *uniFiNetwork, target uniFiNetworkTarget) error {
	updated := make(map[string]any, len(network.raw)+4)
	for key, value := range network.raw {
		updated[key] = value
	}
	updated["ip_subnet"] = target.subnet
	updated["dhcpd_enabled"] = target.dhcpEnabled
	if target.dhcpEnabled {
		updated["dhcpd_start"] = target.dhcpStart
		updated["dhcpd_stop"] = target.dhcpStop
	}

	body, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	responseBody, err := session.request("PUT", networkConfPath(network.stringField("_id")), body)
	if err != nil {
		return fmt.Errorf("unable to update UniFi network %q: %v", network.stringField("name"), err)
	}
	if _, err = parseUniFiData(responseBody); err != nil {
		return fmt.Errorf("unable to update UniFi network %q: %v", network.stringField("name"), err)
	}
	network.raw = updated
	return nil
}

// Returns the VLAN ID of the network, which UniFi may report as either a number or a string.
func (network *uniFiNetwork) vlan() (int, bool) {
	switch value := network.raw["vlan"].(type) {
	case float64:
		return int(value), true
	case string:
		vlan, err := strconv.Atoi(value)
		return vlan, err == nil
	default:
		return 0, false
	}
}

func (network *uniFiNetwork) stringField(key string) string {
	value, _ := network.raw[key].(string)
	return value
}

// Returns true if the network already has the given target configuration.
func (network *uniFiNetwork) matches(target uniFiNetworkTarget) bool {
	if network.stringField("ip_subnet") != target.subnet {
		return false
	}
	dhcpEnabled, _ := network.raw["dhcpd_enabled"].(bool)
	if dhcpEnabled != target.dhcpEnabled {
		return false
	}
	if target.dhcpEnabled {
		return network.stringField("dhcpd_start") == target.dhcpStart &&
			network.stringField("dhcpd_stop") == target.dhcpStop
	}
	return true
}
