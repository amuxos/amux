package extensions

import "github.com/amuxos/amux/internal/modelservice"

type ModelService = modelservice.Service
type ModelServiceState = modelservice.State
type ModelServiceModel = modelservice.Model

var RegisterModelService = modelservice.Register
var StartModelServiceGateway = modelservice.StartGateway
var StartModelServicePassthroughGateway = modelservice.StartPassthroughGateway

var SetModelServiceSessionQuotaBorrowed = modelservice.SetSessionQuotaBorrowed
var IsModelServiceSessionQuotaBorrowed = modelservice.IsSessionQuotaBorrowed
var ClearModelServiceSessionQuotaBorrowed = modelservice.ClearSessionQuotaBorrowed

var NewModelServiceProxyError = modelservice.NewProxyError
var ModelServiceProxyErrorStatus = modelservice.ProxyErrorStatus
