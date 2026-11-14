package serving

import (
	"encoding/json"
	"fmt"

	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// Test profiles carry session parameters the way a stored session result
// does; only the derivation constructs them, so fixtures decode its wire form.
func schedulerTestParameter(wire string) derivation.SessionParameter {
	var p derivation.SessionParameter
	if err := json.Unmarshal([]byte(wire), &p); err != nil {
		panic(err)
	}
	return p
}

func schedulerTestIdentified(value float64) derivation.SessionParameter {
	return schedulerTestParameter(fmt.Sprintf(`{"status":"Identified","value":%v}`, value))
}

func schedulerTestNotIdentified(reason string) derivation.SessionParameter {
	return schedulerTestParameter(fmt.Sprintf(`{"status":"NotIdentified","reason":%q}`, reason))
}

// schedulerTestSlope is an identified input slope in joules per input.
func schedulerTestSlope(joules float64) *derivation.SessionParameter {
	p := schedulerTestIdentified(joules)
	return &p
}

// schedulerTestPower is a node power model with settled idle watts and no
// awake step: node load watts are then the input slope times the input rate,
// the energy the fixtures were written against before the awake term.
func schedulerTestPower(idle float64) *ProfilePower {
	return schedulerTestAwakePower(idle, 0, 0)
}

func schedulerTestAwakePower(idle, step, hold float64) *ProfilePower {
	return &ProfilePower{IdleWatts: schedulerTestIdentified(idle), AwakeStepWatts: schedulerTestIdentified(step), HoldSeconds: schedulerTestIdentified(hold)}
}

// schedulerTestModel is the power model of a hand-built capacity item: no
// awake step and one batch with input slope e, so its load watts are e*rate.
func schedulerTestModel(e float64) *schedulerLoadModel {
	return &schedulerLoadModel{identity: fmt.Sprint("test/", e), stepOK: true, holdOK: true,
		batches: []schedulerLoadBatch{{slope: e, slopeOK: true, busyOK: true}}}
}
