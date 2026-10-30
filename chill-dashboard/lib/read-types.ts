export type Condition = {type:string;status:string;reason?:string;message?:string;lastTransitionTime?:string;observedGeneration?:number}
export type PageInfo = {offset:number;limit:number;total:number;nextOffset?:number}
export type SystemView = {name:string;uid:string;resourceVersion:string;generation:number;creationTimestamp:string;spec:Record<string,unknown>;status:{conditions?:Condition[];[key:string]:unknown}}
export type SystemsView = {readAt:string;resourceVersion:string;systems:SystemView[];page?:PageInfo}
export type InventoryNode = {name:string;uid:string;resourceVersion:string;ready:string;schedulingDisabled:boolean}
// api/v1alpha1/deviceclass_types.go: exactly the target matching kind is present.
export type PowerModeActuator = {kind:"Fixed"} | {kind:"JetsonNVPModel";jetsonNVPModel:{id:number}} | {kind:"NvidiaLockedGPUClock";nvidiaLockedGPUClock:{minMHz:number;maxMHz:number;mode?:0|1}}
export type InventoryClass = {name:string;uid:string;resourceVersion:string;nodeSelector:Record<string,string>;architecture:string;memory:string;accelerator?:string;powerModes:{name:string;actuator:PowerModeActuator}[];nodes:InventoryNode[]}
export type InventoryView = {readAt:string;deviceClassesResourceVersion:string;nodesResourceVersion:string;modelSpecsResourceVersion:string;classes:InventoryClass[];models:{name:string;uid:string;resourceVersion:string;spec:unknown}[];classPage?:PageInfo;modelPage?:PageInfo}
export type NodeRef = {name:string;uid:string}
export type WorkloadRef = {name:string;uid:string}
export type InstanceState = {id:string;targetID?:string;ownerWorkloadUID?:string;node:NodeRef;profile:string;ready:boolean;stopped:boolean;reason?:string;workloads:WorkloadRef[]|null;waiting:Record<string,number>;running:Record<string,number>;assignedUnknown:Record<string,number>;observedAt:string;native?:unknown}
// internal/serving/httpapi: GET v1/pools; GET v1/pools/{workload UID} returns one ActivePool.
export type ActivePool = {workload:WorkloadRef;nodes:NodeRef[]|null;instances:InstanceState[]|null}
export type PoolNode = {node:NodeRef;ready:boolean;powerMode:string;reasons:string[]|null}
export type PoolsView = {workloads:ActivePool[]|null;measurement:{node:NodeRef;instances:InstanceState[]|null}[]|null;available:PoolNode[]|null;other:PoolNode[]|null;issues:{node?:string;code?:string;message?:string;[key:string]:unknown}[]|null;observedAt:string}
export type ProfilingRun = {namespace:string;name:string;uid:string;resourceVersion:string;generation:number;creationTimestamp:string;spec:Record<string,unknown>;status:{conditions?:Condition[];[key:string]:unknown}}
export type RunsView = {readAt:string;resourceVersion:string;namespace:string;allNamespaces:boolean;runs:ProfilingRun[];page?:PageInfo}
export type ObservationCohort = {definition:string;total:number;outcomes:Record<string,number>;sampleCount:number;latencyNanos:WireInteger[];completionResidualNanos:number[]|null;percentile?:number;quantileNanos:WireInteger|null;targetLatencyNanos?:WireInteger}
// Batch stage spans (preprocess/backend/postprocess/firstToken) are shared by the batch; units is per request.
export type BackendProgress = {requestID:string;workloadUID:string;definition:string;phase:string;at:string;outcome?:string;error?:string;batchID?:WireInteger;batchSize?:number;profileID?:string;decisionID?:string;queueMS:number;serviceMS:number;preprocessMS:number;backendMS:number;postprocessMS:number;firstTokenMS?:number;units?:number;nativeElapsedMS:number;executionComplete:boolean}
// One request per row; kind "" is still open in memory. *Nanos count from receivedAt; absent = stage not reached.
export type RequestRow = {kind:"assigned"|"final"|"";requestID:string;workloadUID?:string;definitionID?:string;node?:NodeRef;instance?:string;profileID?:string;decisionID?:string;outcome?:string;error?:string;executionComplete?:boolean;clientSpanNanos?:WireInteger;receivedAt:string;lifetimeNanos?:WireInteger;dispatchedAt?:string;elapsedNanos?:WireInteger;validatedNanos?:WireInteger;acceptedNanos?:WireInteger;dispatchedNanos?:WireInteger;queuedNanos?:WireInteger;runningNanos?:WireInteger;executedNanos?:WireInteger;exchangeNanos?:WireInteger;batchID?:WireInteger;batchSize?:number;queueMS?:number;serviceMS?:number;backendMS?:number;preprocessMS?:number;postprocessMS?:number;firstTokenMS?:number;units?:number;nativeElapsedMS?:number}
export type ManagementEvent = {workloadUID:string;kind:string;phase:string;before?:unknown;after?:unknown;target?:unknown;at:string;error?:string;actorUsername?:string;actorUID?:string}
export type ObservationDefinition = {identity:string;definition:SchedulerDefinition}
export type ObservationResult = {uid:string;from:string;to:string;definitionFilter:string[];outcomeFilter:string[];total:number;selected:number;outcomes:Record<string,number>;cohorts:ObservationCohort[];definitions:ObservationDefinition[];requests:RequestRow[];management:ManagementEvent[];nextCursor?:string;quantileMethod:string;samplePopulation:string;gapUntil?:string}
// One raw profile batch execution (run.NativeServiceSample); recordIDs and units are per request of the batch.
export type NativeSample = {batchID:WireInteger;batchSize:number;recordIDs:string[]|null;units:number[]|null;runnerServiceMs:number;preprocessMs:number;backendCallMs:number;postprocessMs:number;firstTokenMs?:number;nativeRequestMs:number}
export type Passport = {apiVersion:string;kind:string;readAt:string;run:{namespace:string;name:string;uid:string;resourceVersion:string;generation:number;spec:unknown;conditions?:Condition[];job?:unknown;actualExecution?:unknown};evidence:{finalizedAt:string;final:unknown;raw:unknown};measurement:{runtime:{family:string;version:string;provider:string;architecture:string;batch:number;warmupIterations:number;measurementStartedAt:string;measurementEndedAt:string;elapsedSeconds:number;completedCalls:number;completedItems:number;throughputItemsPerSecond:number;latencyMilliseconds:{count:number;mean:number;p50:number;p95:number;p99:number};nativeSamples?:NativeSample[]|null};power:{source:{kind:string;nodeName?:string;metric:string};startedAt:string;endedAt:string;observationSeconds:number;samples:number;failures:number;watts?:{minimum:number;mean:number;maximum:number};maximumSampleGapSeconds:number;sourceTimestampAbsent:boolean};thermal:unknown;workload?:unknown}}
export type CampaignResult = {apiVersion:string;kind:string;readAt:string;campaign:{namespace:string;name:string;uid:string;resourceVersion:string;generation:number;spec:unknown;phase?:string;outcome?:string;completedTrials:number;conditions?:Condition[];storedResult:unknown};result:unknown}

// Scheduler wire integers retain int64/uint64 literals beyond Number's range.
export type WireInteger = number | bigint
export type SchedulerSettings = {
  observationWindowNanos:WireInteger; controlIntervalNanos:WireInteger; comparisonPeriodNanos:WireInteger; maxObservationAgeNanos:WireInteger
  bootstrapCapacityWaitNanos:WireInteger
  replacementDwellNanos:WireInteger
  scaleDownStabilizationNanos:WireInteger; transitionReserveFraction:number
  maxReplicas:WireInteger; maxInstances:WireInteger; maxCPUs:WireInteger; servingMemoryBytes:WireInteger
  completionMarginMS:number
  capacityWindowNanos:WireInteger; capacityWindows:WireInteger; capacityPassingWindows:WireInteger; capacityMinimumRequests:WireInteger
  planSearchWorkers:WireInteger
  autonomousWorkloads:string[]|null
  minimumDemand?:Record<string,number>
  // Nodes the planner never changes (reason ExcludedNode).
  excludedNodes:string[]|null
  controls:{placement:boolean;resources:boolean;replicas:boolean;routing:boolean;batching:boolean;power:boolean;membership:boolean}
  // Only GET settings carries it: the stored declaration, read from the ConfigMap.
  staticRouting?:Routing
  // Accepted power declarations, read-only; never sent back in settings PUT.
  meters?:{id:string;node:NodeRef;scope:string;source:PowerSource}[]
}
export type SchedulerDefinition = {name:string;uid:string;generation:WireInteger;model:{name:string;uid:string;generation:WireInteger;paths:unknown[]|null};slo:{percentile:number;targetLatencyNanos:WireInteger}}
export type SchedulerAllocation = {cpus:number[]|null;gpuUUID?:string;memoryBytes:WireInteger;threads:number;cpuCount:number;cpuReserved:boolean}
export type SchedulerNode = {node:NodeRef;powerMode:string;ready:boolean;cpus:number[]|null;gpuUUIDs:string[]|null;memoryBytes:WireInteger;reserved:SchedulerAllocation;nonServing:SchedulerAllocation[]|null;observedAt:string;reasons?:string[];measurement?:boolean}
// internal/profiling/derivation/curve_plan.go CurveProtocolContract; idle protocols have no dataset.
export type CurveProtocol = {workloadMode:string;dataset?:{name:string;uid:string};warmupIterations:number;settlingDurationSeconds:number;durationSeconds:number;metricIntervalMilliseconds:number;metricTimeoutMilliseconds:number;metricDurationSeconds:number;metricObserverImage:string;workloadObserverImage:string;workloadSchemaVersion:string}
// internal/profiling/derivation/curve_plan.go CurvePlanSpec.
export type CurvePlanSpec = {name:string;logicalModel:string;deviceClass:string;nodeUID:string;runtimeFamily:string;backend:string;firstfloor?:{maxBatchSize:number;maxQueueSize:number;deadlinePolicy?:string};runtimeAccess?:{privileged:boolean;supplementalGroups?:number[];resources?:Record<string,string>;gpuUUID?:string};runtimeImage:string;powerMode:string;cpu:{policy:string;policyVersion:string;request:string;limit:string;ortIntraOpThreads:number;ortInterOpThreads:number;affinityCPUs?:number[];exclusivityClaimed:boolean};metricSource:{sourceKind:string;nodeName:string;endpoint:string;powerMetric:string};activeProtocol:CurveProtocol;idleProtocol:CurveProtocol;conversion:{targetPlatform?:string;tool?:string;toolVersion?:string};artifact:{executionPath:string;name:string;files:string[]|null;image:string};preprocessor:{name:string;image:string;architecture:string;path:string;maxNewTokens?:number;inputs:string[]|null;outputs:string[]|null};plannedBatchSizes:number[]|null}
export type SchedulerProfile = {id:string;reference:unknown;evidence:unknown[]|null;nodeName:string;condition:CurvePlanSpec;batches:{batch:number;durationNanos:number[]|null;energyPerInputJoules:number;memoryBytes:WireInteger}[]|null;confidenceLevel:number;idleBaselineWatts:number;measuredAt:string;runtimeMetadata?:unknown;metricSource:PowerSource}
// Stored decision records carry profileID only; GET v1/scheduler/profiles serves the profile body.
export type SchedulerInstance = {id:string;node:NodeRef;profile?:SchedulerProfile|null;profileID?:string;workloadUID:string;allocation:SchedulerAllocation;ready:boolean;stopped:boolean;capacity:number;weight:number;maxBatch:number;maxWaitNanos:WireInteger;observedAt:string;idleMeter:string;idleScope:string;unroutedSince?:string;heldUntil?:string}
export type SchedulerRequest = {id:string;queueOrder:number;remainingLifetimeNanos?:WireInteger;definition:SchedulerDefinition|null;definitionID?:string;receivedAt:string;elapsedNanos:WireInteger;queueAgeNanos:WireInteger;phase:string;instance?:string;batchID?:string;runningElapsedNanos:WireInteger;returnElapsedNanos:WireInteger;arrivalRate:number;nativeSenderElapsedMS?:number;nativeOutcome?:string}
export type SchedulerArrival = {id:string;lifetimeNanos:WireInteger;at:string;definition?:SchedulerDefinition;accepted:boolean;compute:boolean;workloadUID?:string;outcome?:string}
export type NodeBaseline = {id:string;node:NodeRef;powerMode:string;state:string[]|null;watts:number;wattsUncertainty?:number;meter:string;scope:string;measuredAt:string;reference:unknown}
export type OperationCost = {id:string;node:NodeRef;kind:string;from:string;to:string;trials:{durationNanos:WireInteger;joules:number;energyUncertaintyJoules?:number}[]|null;energyScope:string;subtractedWatts?:number;meter:string;scope:string;reference:unknown;powerMode?:string;beforeState:string[]|null;targetState:string[]|null;beforeBaselineWatts?:number;measuredAt:string}
export type SchedulerAction = {id:string;kind:string;node:NodeRef;instance?:string;from?:string;to?:string;dependsOn:string[]|null;costID?:string;status:string;elapsedNanos:WireInteger;cancelable:boolean;reason?:string;fixed?:boolean}
export type SchedulerEstimate = {uid:string;definition:string;samples:number;percentile:number;expectedNanos:number|null;targetNanos:WireInteger;normalizedExceedance:number;predictedOutcomes:Record<string,number>|null}
export type SchedulerCandidate = {id:string;keep:boolean;nodes?:SchedulerNode[]|null;instances?:SchedulerInstance[]|null;routing?:Record<string,Record<string,number>>|null;actions:SchedulerAction[]|null;status:string;reasons:string[]|null;slo?:SchedulerEstimate[]|null;expectedJoules:number|null;worstExceedance:number;assumptions?:string[]|null;sources?:string[]|null}
export type SchedulerReservation = {owner:string;nodes:NodeRef[]|null;workloadUIDs:string[]|null;podUIDs:string[]|null;instances:string[]|null;target?:SchedulerCandidate;reason?:string}
export type SchedulerDownState = {rate:number;windowMax:number;windowNanos:WireInteger;supply:number;allowed:boolean;reason:string;resetReason?:string}
export type SchedulerSnapshot = {at:string;workloads:(SchedulerDefinition|null)[]|null;nodes:SchedulerNode[]|null;instances:SchedulerInstance[]|null;routing:Record<string,Record<string,number>>|null;routingCredits:Record<string,Record<string,WireInteger>>|null;outstanding:SchedulerRequest[]|null;arrivals:SchedulerArrival[]|null;options:{instance:SchedulerInstance;compatibleUIDs:string[]|null}[]|null;baselines:NodeBaseline[]|null;costs:OperationCost[]|null;actions:SchedulerAction[]|null;reasons:string[]|null;references:string[]|null;reservations?:SchedulerReservation[];scaleDown?:Record<string,SchedulerDownState>;emptyNodeScaleDownAllowed?:boolean}
// state: Disabled, MeasurementEvidenceMissing, Controlled, or Fixed.
export type SchedulerWorkloadState = {uid:string;state:string;reason?:string}
// internal/serving/scheduler_capacity.go SchedulerCapacity; a missing wait, drain or exceedance is unbounded.
export type SchedulerCapacityWorkload = {uid:string;rate:number;minimumDemand:number;queued:number;kappaNew:number;kappaRetained:number;backlog:number;waitSeconds?:number;drainSeconds?:number;finalExceedance?:number;transitionExceedance?:number;weights:Record<string,number>|null;rows:string[]|null;scaleDown?:SchedulerDownState}
export type SchedulerNodePlan = {node:NodeRef;key:string;eligible:boolean;reason?:string;tempo?:"fast"|"slow";kappa?:Record<string,number>;kappaRetained?:Record<string,number>;preNanos:WireInteger;postNanos:WireInteger;joules:number;energyUncertaintyJoules?:number}
// search is the plan search scope: neighborhood k reached, plan sets evaluated, time, and whether the budget cut it.
export type SchedulerPlanSearch = {neighborhood:number;evaluated:number;durationNanos:WireInteger;budgetCut?:boolean}
export type SchedulerCapacity = {periodNanos:WireInteger;routeNanos:WireInteger;search?:SchedulerPlanSearch;workloads:SchedulerCapacityWorkload[]|null;plans:SchedulerNodePlan[]|null;gainUncertaintyJoules?:number}
export type SchedulerDecision = {at:string;status:string;keep:SchedulerCandidate;target:SchedulerCandidate|null;reasons:string[]|null;settings:SchedulerSettings;workloads:SchedulerWorkloadState[]|null;search:{examined:number;bounds:SchedulerSettings;windowStart:string;windowEnd:string;received:number;accepted:number;computeInputs:number;population:string;loads:{uid:string;received:number;accepted:number;computeInputs:number;inputsPerSecond:number}[]|null;comparedNodes:NodeRef[]|null};references:string[]|null;capacity?:SchedulerCapacity;evaluationElapsedNanos?:WireInteger}
// Weights per workload UID and instance ID; original weights, not probabilities.
export type Routing = Record<string,Record<string,number>>
export type SchedulerOperation = {id:string;kind:string;node:NodeRef;instance?:string;prepare?:{name:string;instance:SchedulerInstance;bundle:unknown;store:unknown;config:unknown;memoryBytes:WireInteger};actuation?:unknown;target?:SchedulerCandidate;sourceConfigRV?:string;staticRouting?:Routing;settingsValidatedAt?:string}
// Pods carrying this label were placed by CHILL: internal/profiling/run/workload.go
export const workloadRoleLabel = "edge.dacs.io/workload-role"
export type WorkloadPod = {namespace:string;name:string;uid:string;phase:string;images:string[];labels?:Record<string,string>}
export type OperationState = {at:string;node:NodeRef;powerMode:string;instances:InstanceState[]|null;native:Record<string,unknown>|null;pods:WorkloadPod[]|null;states:string[]|null;reasons?:string[];routing?:Routing}
export type OperationResult = {operation:SchedulerOperation;before:OperationState;actual:OperationState;startedAt:string;endedAt:string;status:string;error?:string;progress:string[]|null;drainNative?:Record<string,unknown>}
// code: CostEvidenceInvalid or MeterUndeclared; source is a store key or a measurement namespace/name.
export type CostIssue = {source:string;code:string;reason:string}
export type SchedulerControllerState = {snapshot:SchedulerSnapshot;decision:SchedulerDecision;actions:SchedulerAction[]|null;results:OperationResult[]|null;costIssues?:CostIssue[]|null}
export type SchedulerView = {settings:SchedulerSettings;state:SchedulerControllerState}
export type SchedulerSettingsChange = {actorUsername:string;actorUID:string;before:SchedulerSettings;after:SchedulerSettings;beforeStaticRouting?:Routing;afterStaticRouting?:Routing;reason?:string;validatedAt:string;appliedAt?:string;affected:NodeRef[]|null;error?:string}
export type SchedulerActionObservation = {id:string;kind:string;node:NodeRef;instance?:string;phase:string;startedAt:string;completedAt?:string;expected?:unknown;actual?:unknown;error?:string;evidence?:unknown[]}
export type PowerSource = {kind:string;nodeName?:string;namespace?:string;podName?:string;endpoint:string;metric:string;metricLabels?:Record<string,string>;thermalEndpoint?:string;thermalMetrics?:unknown}
export type PowerResult = {source:PowerSource;startedAt:string;endedAt:string;samples:{observedAt:string;watts:number;thermal?:unknown;requestLatencySeconds:number}[]|null;failures:{observedAt:string;requestLatencySeconds:number;reason:string;message:string}[]|null;summary:{attempts:number;successfulSamples:number;failures:number;observationDurationSeconds:number;meanRequestLatencySeconds:number;p95RequestLatencySeconds:number;maximumSampleGapSeconds:number;sourceTimestampAbsent:boolean}}
export type SchedulerObservation = {at:string;decision?:SchedulerDecision;snapshot?:SchedulerSnapshot;settings?:SchedulerSettingsChange;action?:SchedulerActionObservation;power?:PowerResult;node?:NodeRef;meter?:string;powerScope?:string;powerSampling?:{Interval:WireInteger;Duration:WireInteger;RequestTimeout:WireInteger;MaxEvents:number};powerCoverage?:{MaximumBoundaryDistance:WireInteger;MinimumCoverage:number;MaximumGap:WireInteger};powerJoules?:number;powerError?:string}
export type SchedulerObservationPage = {from:string;to:string;events:SchedulerObservation[]|null;nextCursor?:string;gapUntil?:string}
export type OperatingPowerNode = {node:NodeRef;meter:string;scope:"node-exclusive-wall";joules:number|null;meanWatts:number|null;samples:number;sourceTimestampAbsent:boolean;reasons:string[]}
export type OperatingPower = {from:string;to:string;scope:"whole-pool-wall";nodes:OperatingPowerNode[];wholePoolJoules:number|null;wholePoolMeanWatts:number|null;activePoolJoules:number|null;activePoolMeanWatts:number|null;complete:boolean;reasons:string[];gapUntil?:string}
export type LoadWindow = {from:string;to:string;durationNanos:WireInteger;population:string;workloads:{uid:string;received:number;accepted:number;assigned:number;completed:number;receivedInputsPerSecond:number;acceptedInputsPerSecond:number;assignedInputsPerSecond:number;completionEventsPerSecond:number;instanceAssignments:Record<string,number>|null}[]|null;arrivals:SchedulerArrival[]|null;gapUntil?:string}
// internal/serving/httpapi/profiles.go: the latest profile per measured condition and the measurements that contribute none.
// source is a Go types.NamespacedName, encoded without JSON tags.
export type CatalogIssue = {source:{Namespace:string;Name:string};code:string;reason:string}
export type ProfilesView = {configVersion:string;profiles:SchedulerProfile[]|null;insufficiencies:CatalogIssue[]|null;preparations:Record<string,unknown>|null}
// BatchEnergyOptimization as served by GET/POST/DELETE /api/v1/namespaces/{ns}/batch-energy-optimizations.
export type MeasurementProtocol = {warmupIterations?:number;durationSeconds?:number;settlingDurationSeconds?:number;dataset?:string;metricObservation?:{intervalMilliseconds:number;timeoutMilliseconds:number;durationSeconds:number}}
export type MeasurementPlanSpec = {state:{candidate:{modelSpec:string;preprocessor:string;deviceClass:string;executionPath:string;powerMode:string};targetNode:string;cpuRequest?:string};activeProtocol:MeasurementProtocol;idleProtocol:MeasurementProtocol;cpuQuota?:string;cpuAffinity?:number[];trialsPerBatch:number;batchSizes?:number[];retryLimit:number}
export type MeasurementPlan = {apiVersion?:string;kind?:string;metadata:{name:string;namespace:string;uid:string;resourceVersion:string;creationTimestamp:string;labels?:Record<string,string>};spec:MeasurementPlanSpec;status?:{phase?:"Measuring"|"Complete"|"Failed"|string;completedTrials?:number;outcome?:string;conditions?:Condition[];result?:unknown}}
export type MeasurementPlanList = {items:MeasurementPlan[]|null}
export type MeasurementMutation = {request:"Accepted"|"NOT_SUBMITTED"|"DeletionRequested"|string;task:MeasurementPlan|null;evidenceDeleted?:boolean;deletedEvidence?:{store:string;key:string}[]}
// internal/operatorconsole/batch_energy_optimization_preflight_view.go: plan only when resolves, reason only when not.
export type MeasurementPreflight = {apiVersion:string;kind:string;readAt:string;campaign:{namespace:string;name:string;spec:MeasurementPlanSpec};resolves:boolean;reason?:string;plan?:{schemaVersion:string;shared:CurvePlanSpec}}
