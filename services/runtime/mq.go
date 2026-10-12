package runtime

// Model change event types (AD5).
const (
	ModelEventUpsert = "upsert"
	ModelEventDelete = "delete"
)

// SchemeMessage is the runtime.scheme payload (AD6) the runtime service
// broadcasts and the controller consumes.
type SchemeMessage struct {
	Scheme    string `json:"scheme"`
	Revision  int64  `json:"revision"`
	UpdatedAt string `json:"updated_at"`
	UpdatedBy string `json:"updated_by"`
}

// ModelChangeMessage is the runtime.models.changes payload (AD5): the
// desired-state registration change the controller reconciles into an
// AIBrix ModelClaim.
type ModelChangeMessage struct {
	EventType   string            `json:"event_type"` // upsert | delete
	ModelName   string            `json:"model_name"`
	Engine      string            `json:"engine,omitempty"`
	Replicas    int64             `json:"replicas,omitempty"`
	ArtifactURL string            `json:"artifact_url,omitempty"`
	PodSelector map[string]string `json:"pod_selector,omitempty"`
	EngineArgs  map[string]string `json:"engine_args,omitempty"`
}
