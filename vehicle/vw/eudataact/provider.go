package eudataact

import (
	"strconv"
	"strings"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util"
)

const (
	// portalInterval is the cadence at which the portal delivers a new dataset
	portalInterval = 15 * time.Minute
	// portalLatency is the margin added to a dataset's timestamp before the
	// following dataset is expected to be available for download
	portalLatency = 30 * time.Second
)

// Provider implements the vehicle api on top of the EU Data Act dataset.
//
// The portal is not a live api: it stores a new dataset roughly every
// portalInterval and only ever appends. Rather than re-reading the full history
// on every poll, a store downloads each dataset once and merges its data points
// into a single map, keeping the newest value per field across all datasets. The
// status getter is cached; instead of relying on the cache ttl alone, each read
// schedules a reset for the moment the next dataset is expected (the dataset's
// timestamp plus portalInterval and a latency margin), so the map is updated as
// soon as the portal delivers a new dataset.
type Provider struct {
	log     *util.Logger
	statusG func() (map[string]point, error)
}

// NewProvider creates a vehicle api provider
func NewProvider(api *API, vin string, cache time.Duration) *Provider {
	v := &Provider{log: api.log}
	s := sharedStore(api)

	var cached util.Cacheable[map[string]point]
	cached = util.ResettableCached(func() (map[string]point, error) {
		ts, err := s.update(vin)
		if err != nil {
			return nil, err
		}
		if !ts.IsZero() {
			time.AfterFunc(resetDelay(ts, time.Now()), cached.Reset)
		}
		data := s.snapshot(vin)
		if p := lookup(data, FieldCarCapturedTime); p != nil {
			if t, err := time.Parse(time.RFC3339, p.Value); err == nil {
				if age := time.Since(t); age > 4*portalInterval {
					v.log.DEBUG.Printf("vehicle data is stale: car last reported %s ago", age.Round(time.Minute))
				}
			}
		}
		return data, nil
	}, cache)

	v.statusG = cached.Get

	return v
}

// resetDelay returns the delay until the dataset following the one delivered at
// ts is expected to be available. It never returns less than portalLatency so a
// late or repeated dataset does not cause immediate re-polling.
func resetDelay(ts, now time.Time) time.Duration {
	if d := ts.Add(portalInterval + portalLatency).Sub(now); d > portalLatency {
		return d
	}
	return portalLatency
}

// lookup returns the first present, non-empty value among the given field names
func lookup(data map[string]point, fields ...string) *point {
	for _, f := range fields {
		if v, ok := data[f]; ok {
			return new(v)
		}
	}
	return nil
}

var _ api.Battery = (*Provider)(nil)

// Soc implements the api.Battery interface
func (v *Provider) Soc() (float64, error) {
	data, err := v.statusG()
	if err != nil {
		return 0, err
	}

	if p := lookup(data, FieldBatteryStateReportSoc, FieldSoc, FieldHvSoc, FieldHvBatteryLevel); p != nil {
		return strconv.ParseFloat(p.Value, 64)
	}

	return 0, api.ErrNotAvailable
}

var _ api.VehicleRange = (*Provider)(nil)

// Range implements the api.VehicleRange interface
func (v *Provider) Range() (int64, error) {
	data, err := v.statusG()
	if err != nil {
		return 0, err
	}

	if p := lookup(data, FieldRangeSecondary, FieldRangePrimary, FieldRangeCombined); p != nil {
		f, err := strconv.ParseFloat(p.Value, 64)
		return int64(f), err
	}

	return 0, api.ErrNotAvailable
}

var _ api.VehicleFinishTimer = (*Provider)(nil)

// FinishTime implements the api.VehicleFinishTimer interface
func (v *Provider) FinishTime() (time.Time, error) {
	data, err := v.statusG()
	if err != nil {
		return time.Time{}, err
	}

	// new format: absolute finish time
	if p := lookup(data, FieldFinishTime); p != nil {
		return time.Parse(time.RFC3339, p.Value)
	}
	// new format: remaining seconds with "s" suffix
	if p := lookup(data, FieldRemainingTimeAlt); p != nil {
		val := strings.TrimSuffix(p.Value, "s")
		if secs, err := strconv.ParseInt(val, 0, 64); err == nil {
			return time.Now().Add(time.Duration(secs) * time.Second), nil
		}
	}
	// old format: remaining minutes as integer offset from data timestamp
	if p := lookup(data, FieldRemainingTime); p != nil && p.Value != "65535" {
		if v, err := strconv.ParseInt(p.Value, 0, 64); err == nil {
			return p.Timestamp.Add(time.Duration(v) * time.Minute), nil
		}
	}

	return time.Time{}, api.ErrNotAvailable
}

var _ api.VehicleOdometer = (*Provider)(nil)

// Odometer implements the api.VehicleOdometer interface
func (v *Provider) Odometer() (float64, error) {
	data, err := v.statusG()
	if err != nil {
		return 0, err
	}

	if p := lookup(data, FieldOdometer, FieldOdometerValue); p != nil {
		return strconv.ParseFloat(p.Value, 64)
	}

	return 0, api.ErrNotAvailable
}

var _ api.ChargeState = (*Provider)(nil)

// Status implements the api.ChargeState interface
func (v *Provider) Status() (api.ChargeStatus, error) {
	status := api.StatusA // disconnected

	data, err := v.statusG()
	if err != nil {
		return status, err
	}

	if p := lookup(data, FieldPlugState); p != nil && strings.EqualFold(p.Value, "connected") {
		status = api.StatusB
	}

	if p := lookup(data, FieldChargingState); p != nil &&
		(strings.EqualFold(p.Value, "charging") || strings.EqualFold(p.Value, "conservationCharging")) {
		status = api.StatusC
	}

	if p := lookup(data, FieldCurrentChargeState); p != nil &&
		(strings.Contains(p.Value, "CHARGING_HV") || p.Value == "CHARGE_STATE_CONSERVATION_CHARGING") {
		status = api.StatusC
	}

	return status, nil
}

var _ api.SocLimiter = (*Provider)(nil)

// GetLimitSoc implements the api.SocLimiter interface
func (v *Provider) GetLimitSoc() (int64, error) {
	data, err := v.statusG()
	if err != nil {
		return 0, err
	}

	if p := lookup(data, FieldTargetSoc); p != nil {
		f, err := strconv.ParseFloat(p.Value, 64)
		return int64(f), err
	}

	return 0, api.ErrNotAvailable
}
