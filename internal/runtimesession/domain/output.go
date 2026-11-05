package domain

import "encoding/json"

// Apply обновляет накопленный результат по событию Jupyter, учитывая очистку вывода, display_id и счётчик
// выполнения.
func (e *Execution) Apply(v AgentEvent) error {
	if e.Kind == "inspect" {
		return e.applyInspection(v)
	}
	e.Version++
	if v.Generation != "" {
		e.Generation = v.Generation
	}
	switch v.Kind {
	case "execution_state":
		e.State = v.State
		e.Result = v.Result
		return nil
	case "output_truncated":
		e.Truncated = true
		e.Projection = "pending"
		return nil
	case "kernel_message":
	default:
		return nil
	}
	var c map[string]json.RawMessage
	if json.Unmarshal(v.Content, &c) != nil {
		return ErrProtocol
	}
	if n, ok := c["execution_count"]; ok {
		var count int64
		if json.Unmarshal(n, &count) == nil {
			e.ExecutionCount = &count
			e.Projection = "pending"
		}
	}
	switch v.MsgType {
	case "clear_output":
		var wait bool
		_ = json.Unmarshal(c["wait"], &wait)
		e.ClearPending = wait
		if !wait {
			e.Outputs = []json.RawMessage{}
		}
	case "stream", "execute_result", "display_data", "error":
		if e.ClearPending {
			e.Outputs = []json.RawMessage{}
			e.ClearPending = false
		}
		out := map[string]json.RawMessage{}
		kind, _ := json.Marshal(v.MsgType)
		out["output_type"] = kind
		var keys []string
		switch v.MsgType {
		case "stream":
			keys = []string{"name", "text"}
		case "error":
			keys = []string{"ename", "evalue", "traceback"}
		default:
			keys = []string{"data", "metadata", "execution_count", "transient"}
		}
		for _, k := range keys {
			if value, ok := c[k]; ok {
				out[k] = value
			}
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return ErrProtocol
		}
		e.Outputs = append(e.Outputs, raw)
	case "update_display_data":
		id := displayID(c["transient"])
		if id == "" {
			return nil
		}
		for i, raw := range e.Outputs {
			var out map[string]json.RawMessage
			if json.Unmarshal(raw, &out) != nil {
				return ErrProtocol
			}
			if displayID(out["transient"]) != id {
				continue
			}
			out["data"] = c["data"]
			out["metadata"] = c["metadata"]
			b, err := json.Marshal(out)
			if err != nil {
				return ErrProtocol
			}
			e.Outputs[i] = b
		}
	default:
		return nil
	}
	e.Projection = "pending"
	return nil
}

// displayID извлекает display_id для обновления уже существующего отображаемого результата.
func displayID(raw json.RawMessage) string {
	var v struct {
		ID string `json:"display_id"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.ID
}
