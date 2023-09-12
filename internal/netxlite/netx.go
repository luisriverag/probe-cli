package netxlite

//
// Netx is a high-level structure that provides constructors for basic netxlite
// network operations using a custom model.UnderlyingNetwork.
//

import "github.com/ooni/probe-cli/v3/internal/model"

// TODO(bassosimone,kelmenhorst): we should gradually refactor the top-level netxlite
// functions to operate on a [Netx] struct using a nil-initialized Underlying field.

// Netx allows constructing netxlite data types using a specific [model.UnderlyingNetwork].
type Netx struct {
	// Underlying is the OPTIONAL [model.UnderlyingNetwork] to use. Leaving this field
	// nil makes this implementation functionally equivalent to netxlite top-level functions.
	Underlying model.UnderlyingNetwork
}

// maybeCustomUnderlyingNetwork wraps the [model.UnderlyingNetwork] using a [*MaybeCustomUnderlyingNetwork].
func (netx *Netx) maybeCustomUnderlyingNetwork() *MaybeCustomUnderlyingNetwork {
	return &MaybeCustomUnderlyingNetwork{netx.Underlying}
}
