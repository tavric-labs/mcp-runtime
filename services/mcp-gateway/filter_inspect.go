package main

// inspectFilter captures the bounded body and extracts the RPC method and tool.
func (s *gatewayServer) inspectFilter(in *RequestInput) (InspectedRequest, *FilterResponse) {
	return InspectedRequest{RequestInput: in, Inspection: inspectRPCRequest(in.R)}, nil
}
