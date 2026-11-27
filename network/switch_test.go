// Copyright 2014 Team 254. All Rights Reserved.
// Author: pat@patfairbank.com (Patrick Fairbank)

package network

import (
	"encoding/json"
	"fmt"
	"github.com/Team254/cheesy-arena/model"
	"github.com/stretchr/testify/assert"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakeUniFiApiKey = "api-key-1"

// A fake UniFi OS gateway that implements just enough of the network configuration API to test against. It
// rejects overlapping subnets like the real one does.
type fakeUniFi struct {
	server   *httptest.Server
	mutex    sync.Mutex
	networks []map[string]any
	updates  []string // The name of each network, in the order they were updated.
}

func newFakeUniFi(t *testing.T) *fakeUniFi {
	fake := &fakeUniFi{}
	for i, vlan := range stationVlans {
		network := map[string]any{
			"_id":           fmt.Sprintf("id%d", vlan),
			"name":          fmt.Sprintf("Station %d", vlan),
			"purpose":       "corporate",
			"vlan_enabled":  true,
			"ip_subnet":     fmt.Sprintf("192.168.%d.1/24", vlan),
			"dhcpd_enabled": false,
		}
		// UniFi reports the VLAN as a string on some versions and a number on others.
		if i%2 == 0 {
			network["vlan"] = vlan
		} else {
			network["vlan"] = fmt.Sprintf("%d", vlan)
		}
		fake.networks = append(fake.networks, network)
	}
	// An unrelated network that must never be touched.
	fake.networks = append(
		fake.networks,
		map[string]any{"_id": "idlan", "name": "Default", "ip_subnet": "10.0.100.1/24", "dhcpd_enabled": true},
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /proxy/network/api/s/default/rest/networkconf", fake.handleList)
	mux.HandleFunc("PUT /proxy/network/api/s/default/rest/networkconf/{id}", fake.handleUpdate)
	fake.server = httptest.NewTLSServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (fake *fakeUniFi) authorized(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-API-KEY") != fakeUniFiApiKey {
		http.Error(w, `{"error":{"code":401,"message":"Unauthorized"}}`, http.StatusUnauthorized)
		return false
	}
	return true
}

func (fake *fakeUniFi) handleList(w http.ResponseWriter, r *http.Request) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if !fake.authorized(w, r) {
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"rc": "ok"}, "data": fake.networks})
}

func (fake *fakeUniFi) handleUpdate(w http.ResponseWriter, r *http.Request) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if !fake.authorized(w, r) {
		return
	}
	var updated map[string]any
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	_, newSubnet, err := net.ParseCIDR(updated["ip_subnet"].(string))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, other := range fake.networks {
		if other["_id"] == id {
			continue
		}
		_, otherSubnet, _ := net.ParseCIDR(other["ip_subnet"].(string))
		if newSubnet.Contains(otherSubnet.IP) || otherSubnet.Contains(newSubnet.IP) {
			json.NewEncoder(w).Encode(
				map[string]any{"meta": map[string]any{"rc": "error", "msg": "api.err.NetworkOverlap"}},
			)
			return
		}
	}
	for i, network := range fake.networks {
		if network["_id"] == id {
			fake.networks[i] = updated
			fake.updates = append(fake.updates, updated["name"].(string))
			json.NewEncoder(w).Encode(map[string]any{"meta": map[string]any{"rc": "ok"}, "data": []any{updated}})
			return
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func (fake *fakeUniFi) network(vlan int) map[string]any {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.networks[vlan/10-1]
}

func (fake *fakeUniFi) resetUpdates() {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.updates = nil
}

func (fake *fakeUniFi) updateCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return len(fake.updates)
}

func newTestSwitch(fake *fakeUniFi) *Switch {
	sw := NewSwitch(fake.server.URL, fakeUniFiApiKey)
	sw.configBackoffDuration = time.Millisecond
	return sw
}

func assertStation(t *testing.T, fake *fakeUniFi, vlan int, subnet, dhcpStart, dhcpStop string) {
	network := fake.network(vlan)
	assert.Equal(t, subnet, network["ip_subnet"], "VLAN %d subnet", vlan)
	if dhcpStart == "" {
		assert.Equal(t, false, network["dhcpd_enabled"], "VLAN %d DHCP", vlan)
	} else {
		assert.Equal(t, true, network["dhcpd_enabled"], "VLAN %d DHCP", vlan)
		assert.Equal(t, dhcpStart, network["dhcpd_start"], "VLAN %d DHCP start", vlan)
		assert.Equal(t, dhcpStop, network["dhcpd_stop"], "VLAN %d DHCP stop", vlan)
	}
	// Fields that the code does not manage must be preserved.
	assert.Equal(t, "corporate", network["purpose"])
	assert.Equal(t, true, network["vlan_enabled"])
}

func TestConfigureSwitch(t *testing.T) {
	fake := newFakeUniFi(t)
	sw := newTestSwitch(fake)
	assert.Equal(t, "UNKNOWN", sw.Status)

	// Should give every station a placeholder subnet with DHCP off when no teams are present.
	assert.Nil(t, sw.ConfigureTeamEthernet([6]*model.Team{nil, nil, nil, nil, nil, nil}))
	assert.Equal(t, "ACTIVE", sw.Status)
	for _, vlan := range stationVlans {
		assertStation(t, fake, vlan, fmt.Sprintf("10.255.%d.4/24", vlan), "", "")
	}

	// Should configure one team if only one is present, and leave the others alone.
	fake.resetUpdates()
	assert.Nil(t, sw.ConfigureTeamEthernet([6]*model.Team{nil, nil, nil, nil, {Id: 254}, nil}))
	assertStation(t, fake, 50, "10.2.54.4/24", "10.2.54.20", "10.2.54.199")
	assert.Equal(t, []string{"Station 50"}, fake.updates)

	// Should configure all teams if all are present.
	assert.Nil(
		t,
		sw.ConfigureTeamEthernet([6]*model.Team{{Id: 1114}, {Id: 254}, {Id: 296}, {Id: 1503}, {Id: 1678}, {Id: 1538}}),
	)
	assertStation(t, fake, 10, "10.11.14.4/24", "10.11.14.20", "10.11.14.199")
	assertStation(t, fake, 20, "10.2.54.4/24", "10.2.54.20", "10.2.54.199")
	assertStation(t, fake, 30, "10.2.96.4/24", "10.2.96.20", "10.2.96.199")
	assertStation(t, fake, 40, "10.15.3.4/24", "10.15.3.20", "10.15.3.199")
	assertStation(t, fake, 50, "10.16.78.4/24", "10.16.78.20", "10.16.78.199")
	assertStation(t, fake, 60, "10.15.38.4/24", "10.15.38.20", "10.15.38.199")

	// The unrelated network must be untouched.
	fake.mutex.Lock()
	assert.Equal(t, "10.0.100.1/24", fake.networks[6]["ip_subnet"])
	fake.mutex.Unlock()
	for _, name := range fake.updates {
		assert.NotEqual(t, "Default", name)
	}
}

func TestConfigureSwitchNoChange(t *testing.T) {
	fake := newFakeUniFi(t)
	sw := newTestSwitch(fake)
	teams := [6]*model.Team{{Id: 1114}, {Id: 254}, {Id: 296}, {Id: 1503}, {Id: 1678}, {Id: 1538}}
	assert.Nil(t, sw.ConfigureTeamEthernet(teams))
	assert.Equal(t, 6, fake.updateCount())

	// A second match with the same teams should not reprovision anything.
	fake.resetUpdates()
	assert.Nil(t, sw.ConfigureTeamEthernet(teams))
	assert.Equal(t, 0, fake.updateCount())
	assert.Equal(t, "ACTIVE", sw.Status)
}

func TestConfigureSwitchRotationAndSwap(t *testing.T) {
	fake := newFakeUniFi(t)
	sw := newTestSwitch(fake)
	assert.Nil(
		t,
		sw.ConfigureTeamEthernet([6]*model.Team{{Id: 1114}, {Id: 254}, {Id: 296}, {Id: 1503}, {Id: 1678}, {Id: 1538}}),
	)

	// A full rotation of the teams between stations forms a cycle that needs a placeholder to resolve.
	assert.Nil(
		t,
		sw.ConfigureTeamEthernet([6]*model.Team{{Id: 254}, {Id: 296}, {Id: 1503}, {Id: 1678}, {Id: 1538}, {Id: 1114}}),
	)
	assertStation(t, fake, 10, "10.2.54.4/24", "10.2.54.20", "10.2.54.199")
	assertStation(t, fake, 20, "10.2.96.4/24", "10.2.96.20", "10.2.96.199")
	assertStation(t, fake, 30, "10.15.3.4/24", "10.15.3.20", "10.15.3.199")
	assertStation(t, fake, 40, "10.16.78.4/24", "10.16.78.20", "10.16.78.199")
	assertStation(t, fake, 50, "10.15.38.4/24", "10.15.38.20", "10.15.38.199")
	assertStation(t, fake, 60, "10.11.14.4/24", "10.11.14.20", "10.11.14.199")

	// Two teams swapping stations is the smallest cycle.
	assert.Nil(
		t,
		sw.ConfigureTeamEthernet([6]*model.Team{{Id: 296}, {Id: 254}, {Id: 1503}, {Id: 1678}, {Id: 1538}, {Id: 1114}}),
	)
	assertStation(t, fake, 10, "10.2.96.4/24", "10.2.96.20", "10.2.96.199")
	assertStation(t, fake, 20, "10.2.54.4/24", "10.2.54.20", "10.2.54.199")

	// A team moving into a station that is being vacated in the same match.
	assert.Nil(
		t,
		sw.ConfigureTeamEthernet([6]*model.Team{nil, {Id: 296}, {Id: 1503}, {Id: 1678}, {Id: 1538}, {Id: 1114}}),
	)
	assertStation(t, fake, 10, "10.255.10.4/24", "", "")
	assertStation(t, fake, 20, "10.2.96.4/24", "10.2.96.20", "10.2.96.199")
}

func TestConfigureSwitchErrors(t *testing.T) {
	fake := newFakeUniFi(t)

	// A wrong API key.
	sw := NewSwitch(fake.server.URL, "wrong")
	err := sw.ConfigureTeamEthernet([6]*model.Team{})
	if assert.NotNil(t, err) {
		assert.Contains(t, err.Error(), "status 401")
	}
	assert.Equal(t, "ERROR", sw.Status)
	assert.Equal(t, 0, fake.updateCount())

	// A missing API key and a missing address.
	sw = NewSwitch(fake.server.URL, "")
	err = sw.ConfigureTeamEthernet([6]*model.Team{})
	if assert.NotNil(t, err) {
		assert.Contains(t, err.Error(), "API key is not set")
	}
	sw = NewSwitch("", fakeUniFiApiKey)
	err = sw.ConfigureTeamEthernet([6]*model.Team{})
	if assert.NotNil(t, err) {
		assert.Contains(t, err.Error(), "address is not set")
	}

	// The same team in two stations.
	sw = newTestSwitch(fake)
	err = sw.ConfigureTeamEthernet([6]*model.Team{{Id: 254}, {Id: 254}, nil, nil, nil, nil})
	if assert.NotNil(t, err) {
		assert.Contains(t, err.Error(), "team 254 is assigned to more than one")
	}
	assert.Equal(t, 0, fake.updateCount())

	// A missing station VLAN.
	fake.mutex.Lock()
	fake.networks[2]["vlan"] = 99
	fake.mutex.Unlock()
	err = sw.ConfigureTeamEthernet([6]*model.Team{})
	if assert.NotNil(t, err) {
		assert.Contains(t, err.Error(), "no UniFi network with VLAN ID 30")
	}
	assert.Equal(t, "ERROR", sw.Status)

	// A gateway that cannot be reached.
	sw = NewSwitch("https://127.0.0.1:1", fakeUniFiApiKey)
	assert.NotNil(t, sw.ConfigureTeamEthernet([6]*model.Team{}))
}

func TestConfigureSwitchAddressWithoutScheme(t *testing.T) {
	fake := newFakeUniFi(t)
	sw := NewSwitch(strings.TrimPrefix(fake.server.URL, "https://"), fakeUniFiApiKey)
	sw.configBackoffDuration = time.Millisecond
	assert.Nil(t, sw.ConfigureTeamEthernet([6]*model.Team{{Id: 254}, nil, nil, nil, nil, nil}))
	assertStation(t, fake, 10, "10.2.54.4/24", "10.2.54.20", "10.2.54.199")
}
