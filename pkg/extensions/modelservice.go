package extensions

import "github.com/amuxos/amux/internal/modelservice"

type ModelService = modelservice.Service
type ModelServiceState = modelservice.State
type ModelServiceModel = modelservice.Model

var RegisterModelService = modelservice.Register
var StartModelServiceGateway = modelservice.StartGateway
