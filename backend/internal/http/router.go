package http

import (
	"log/slog"
	stdhttp "net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ikaevus/routegate/backend/internal/agents"
	"github.com/ikaevus/routegate/backend/internal/auth"
	"github.com/ikaevus/routegate/backend/internal/config"
	"github.com/ikaevus/routegate/backend/internal/configs"
	"github.com/ikaevus/routegate/backend/internal/connections"
	"github.com/ikaevus/routegate/backend/internal/dashboard"
	"github.com/ikaevus/routegate/backend/internal/geoip"
	"github.com/ikaevus/routegate/backend/internal/health"
	"github.com/ikaevus/routegate/backend/internal/maintenance"
	"github.com/ikaevus/routegate/backend/internal/nodegroups"
	"github.com/ikaevus/routegate/backend/internal/portal"
	"github.com/ikaevus/routegate/backend/internal/roles"
	"github.com/ikaevus/routegate/backend/internal/routingprofiles"
	"github.com/ikaevus/routegate/backend/internal/servers"
	"github.com/ikaevus/routegate/backend/internal/setup"
	"github.com/ikaevus/routegate/backend/internal/system"
	"github.com/ikaevus/routegate/backend/internal/traffic"
	"github.com/ikaevus/routegate/backend/internal/transfers"
	"github.com/ikaevus/routegate/backend/internal/users"
	"github.com/ikaevus/routegate/backend/internal/vpnaccounts"
)

func NewRouter(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool) stdhttp.Handler {
	mux := stdhttp.NewServeMux()

	healthHandler := health.NewHandler(logger)
	authRepo := auth.NewRepository(pool)
	authHandler := auth.NewHandler(logger, pool, cfg.AuthSessionTTL)
	serversHandler := servers.NewHandler(logger, pool, cfg.PublicURL)
	geoIPHandler := geoip.NewHandler(logger, pool, cfg.GeoIP.Enabled)
	agentsHandler := agents.NewHandler(logger, pool)
	configsHandler := configs.NewHandler(logger, pool)
	dashboardHandler := dashboard.NewHandler(logger, pool)
	usersHandler := users.NewHandler(logger, pool)
	rolesHandler := roles.NewHandler(logger, pool)
	systemHandler := system.NewHandler(logger, pool)
	maintenanceHandler := maintenance.NewHandler(logger, pool)
	setupHandler := setup.NewHandler(logger, pool, cfg.AuthSessionTTL)
	vpnAccountsHandler := vpnaccounts.NewHandler(logger, pool, cfg.PublicURL)
	transfersHandler := transfers.NewHandler(logger, pool)
	vpnAccountNotesHandler := vpnaccounts.NewNotesHandler(logger, pool)
	trafficHandler := traffic.NewHandler(logger, pool)
	connectionsHandler := connections.NewHandler(logger, pool)
	routingProfilesHandler := routingprofiles.NewHandler(logger, pool)
	nodeGroupsHandler := nodegroups.NewHandler(logger, pool)
	portalHandler := portal.NewHandler(logger, pool)
	authn := auth.Middleware(authRepo)
	adminAuth := func(handler stdhttp.HandlerFunc) stdhttp.Handler {
		return authn(auth.RequireAdminSession(stdhttp.HandlerFunc(handler)))
	}
	portalAuth := func(handler stdhttp.HandlerFunc) stdhttp.Handler {
		return authn(auth.RequirePermission("portal:access")(stdhttp.HandlerFunc(handler)))
	}

	mux.HandleFunc("GET /api/admin/health", healthHandler.Get)

	mux.HandleFunc("POST /api/admin/auth/login", authHandler.Login)
	mux.Handle("POST /api/admin/auth/logout", adminAuth(authHandler.Logout))
	mux.Handle("GET /api/admin/me", adminAuth(authHandler.Me))
	mux.Handle("GET /api/v1/auth/me", authn(stdhttp.HandlerFunc(authHandler.Me)))
	mux.Handle("POST /api/v1/auth/initial-setup-token", authn(auth.RequirePermission("system:manage")(stdhttp.HandlerFunc(setupHandler.CreateToken))))
	mux.HandleFunc("POST /api/v1/auth/initial-setup/inspect", setupHandler.Inspect)
	mux.HandleFunc("POST /api/v1/auth/initial-setup/complete", setupHandler.Complete)
	mux.Handle("POST /api/v1/auth/change-password", authn(stdhttp.HandlerFunc(setupHandler.ChangePassword)))
	mux.Handle("POST /api/v1/auth/login", stdhttp.HandlerFunc(authHandler.Login))
	mux.Handle("POST /api/v1/auth/logout", authn(stdhttp.HandlerFunc(authHandler.Logout)))
	mux.Handle("GET /api/v1/auth/sessions", authn(stdhttp.HandlerFunc(authHandler.ListSecuritySessions)))
	mux.Handle("DELETE /api/v1/auth/sessions/{session_id}", authn(stdhttp.HandlerFunc(authHandler.RevokeSecuritySession)))
	mux.Handle("POST /api/v1/auth/sessions/revoke-others", authn(stdhttp.HandlerFunc(authHandler.RevokeOtherSecuritySessions)))
	mux.Handle("GET /api/v1/auth/security-events", authn(stdhttp.HandlerFunc(authHandler.ListSecurityEvents)))
	mux.Handle("DELETE /api/v1/auth/security-events", authn(stdhttp.HandlerFunc(authHandler.ClearSecurityEvents)))

	mux.Handle("GET /api/portal/me", portalAuth(portalHandler.Me))
	mux.Handle("GET /api/portal/dashboard", portalAuth(portalHandler.Dashboard))
	mux.Handle("GET /api/portal/profiles", portalAuth(portalHandler.ListProfiles))
	mux.Handle("GET /api/portal/profiles/{id}", portalAuth(portalHandler.GetProfile))
	mux.Handle("GET /api/portal/profiles/{id}/subscription", portalAuth(portalHandler.GetSubscription))
	mux.Handle("POST /api/portal/profiles/{id}/subscription-token", portalAuth(portalHandler.GenerateSubscriptionAccess))
	mux.Handle("GET /api/portal/profiles/{id}/qr", portalAuth(portalHandler.GetQRCode))
	mux.Handle("GET /api/portal/instructions", portalAuth(portalHandler.ListInstructions))
	mux.Handle("GET /api/portal/instructions/{platform}", portalAuth(portalHandler.GetInstruction))

	mux.Handle("GET /api/admin/servers", adminAuth(serversHandler.LegacyList))
	mux.Handle("GET /api/admin/servers/{id}", adminAuth(serversHandler.LegacyGet))
	mux.Handle("POST /api/admin/servers", adminAuth(serversHandler.LegacyCreate))
	mux.Handle("GET /api/v1/servers", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(serversHandler.List))))
	mux.Handle("POST /api/v1/servers", authn(auth.RequirePermission("servers:create")(stdhttp.HandlerFunc(serversHandler.Create))))
	mux.Handle("GET /api/v1/servers/{server_id}", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(serversHandler.Get))))
	mux.Handle("PATCH /api/v1/servers/{server_id}", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(serversHandler.Update))))
	mux.Handle("DELETE /api/v1/servers/{server_id}", authn(auth.RequirePermission("servers:delete")(stdhttp.HandlerFunc(serversHandler.Delete))))
	mux.Handle("POST /api/v1/servers/{server_id}/geography/auto-detect", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(geoIPHandler.AutoDetect))))
	mux.Handle("GET /api/v1/servers/{server_id}/protocol-settings", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(serversHandler.GetProtocolSettings))))
	mux.Handle("PATCH /api/v1/servers/{server_id}/protocol-settings", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(serversHandler.UpdateProtocolSettings))))
	mux.Handle("POST /api/v1/servers/{server_id}/protocol-settings/recommended", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(serversHandler.ConfigureRecommendedProtocolSettings))))
	mux.Handle("POST /api/v1/servers/{server_id}/protocol-settings/reality-keypair", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(serversHandler.GenerateRealityKeypair))))
	mux.Handle("POST /api/v1/servers/{server_id}/protocol-settings/wireguard-recommended", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(serversHandler.ConfigureRecommendedWireGuard))))
	mux.Handle("GET /api/v1/servers/{server_id}/routing-profile", authn(auth.RequirePermission("routing_profiles:read")(stdhttp.HandlerFunc(routingProfilesHandler.GetServerAssignment))))
	mux.Handle("PUT /api/v1/servers/{server_id}/routing-profile", authn(auth.RequirePermission("routing_profiles:update")(stdhttp.HandlerFunc(routingProfilesHandler.AssignServerProfile))))
	mux.Handle("DELETE /api/v1/servers/{server_id}/routing-profile", authn(auth.RequirePermission("routing_profiles:update")(stdhttp.HandlerFunc(routingProfilesHandler.DeleteServerAssignment))))
	mux.Handle("POST /api/v1/servers/{server_id}/registration-token", authn(auth.RequirePermission("agents:register")(stdhttp.HandlerFunc(serversHandler.CreateRegistrationToken))))
	mux.Handle("POST /api/v1/servers/{server_id}/vpn-core/operations", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(agentsHandler.CreateVPNCoreOperation))))
	mux.Handle("GET /api/v1/servers/{server_id}/vpn-core/operations/{job_id}", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(agentsHandler.GetVPNCoreOperation))))
	mux.Handle("POST /api/v1/servers/{server_id}/vpn-core/installations", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(agentsHandler.CreateVPNCoreInstallation))))
	mux.Handle("GET /api/v1/servers/{server_id}/vpn-core/installations/{job_id}", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(agentsHandler.GetVPNCoreInstallation))))
	registerPlatformUpdateRolloutRoutes(
		mux,
		authn,
		stdhttp.HandlerFunc(agentsHandler.CreatePlatformUpdateRollout),
		stdhttp.HandlerFunc(agentsHandler.GetPlatformUpdateRollout),
		stdhttp.HandlerFunc(agentsHandler.AdvancePlatformUpdateRollout),
	)
	mux.Handle("POST /api/v1/servers/{server_id}/config/render", authn(auth.RequirePermission("configs:render")(stdhttp.HandlerFunc(configsHandler.Render))))
	mux.Handle("GET /api/v1/servers/{server_id}/config/versions", authn(auth.RequirePermission("configs:read")(stdhttp.HandlerFunc(configsHandler.List))))
	mux.Handle("GET /api/v1/servers/{server_id}/config/versions/{version_id}", authn(auth.RequirePermission("configs:read")(stdhttp.HandlerFunc(configsHandler.Get))))
	mux.Handle("POST /api/v1/servers/{server_id}/config/versions/{version_id}/validate", authn(auth.RequirePermission("configs:validate")(stdhttp.HandlerFunc(configsHandler.Validate))))
	mux.Handle("POST /api/v1/servers/{server_id}/config/versions/{version_id}/apply", authn(auth.RequirePermission("configs:apply")(stdhttp.HandlerFunc(configsHandler.Apply))))
	mux.Handle("POST /api/v1/servers/{server_id}/config/versions/{version_id}/reapply", authn(auth.RequirePermission("configs:rollback")(stdhttp.HandlerFunc(configsHandler.Reapply))))
	mux.Handle("DELETE /api/v1/servers/{server_id}/config/versions/{version_id}", authn(auth.RequirePermission("configs:delete")(stdhttp.HandlerFunc(configsHandler.DeleteVersion))))
	mux.Handle("POST /api/v1/servers/{server_id}/config/versions/{version_id}/pin", authn(auth.RequirePermission("configs:delete")(stdhttp.HandlerFunc(configsHandler.PinVersion))))
	mux.Handle("DELETE /api/v1/servers/{server_id}/config/versions/{version_id}/pin", authn(auth.RequirePermission("configs:delete")(stdhttp.HandlerFunc(configsHandler.UnpinVersion))))
	mux.Handle("GET /api/v1/servers/{server_id}/config/apply-jobs", authn(auth.RequirePermission("configs:read")(stdhttp.HandlerFunc(configsHandler.ListApplyJobs))))
	mux.Handle("DELETE /api/v1/servers/{server_id}/config/apply-jobs/completed", authn(auth.RequirePermission("configs:delete")(stdhttp.HandlerFunc(configsHandler.ClearCompletedApplyHistory))))
	mux.Handle("GET /api/v1/servers/{server_id}/config/apply-jobs/{job_id}", authn(auth.RequirePermission("configs:read")(stdhttp.HandlerFunc(configsHandler.GetApplyJob))))

	mux.Handle("GET /api/v1/routing-profiles", authn(auth.RequirePermission("routing_profiles:read")(stdhttp.HandlerFunc(routingProfilesHandler.List))))
	mux.Handle("POST /api/v1/routing-profiles", authn(auth.RequirePermission("routing_profiles:create")(stdhttp.HandlerFunc(routingProfilesHandler.Create))))
	mux.Handle("GET /api/v1/routing-profiles/{profile_id}", authn(auth.RequirePermission("routing_profiles:read")(stdhttp.HandlerFunc(routingProfilesHandler.Get))))
	mux.Handle("PATCH /api/v1/routing-profiles/{profile_id}", authn(auth.RequirePermission("routing_profiles:update")(stdhttp.HandlerFunc(routingProfilesHandler.Update))))
	mux.Handle("DELETE /api/v1/routing-profiles/{profile_id}", authn(auth.RequirePermission("routing_profiles:delete")(stdhttp.HandlerFunc(routingProfilesHandler.Delete))))
	mux.Handle("POST /api/v1/routing-profiles/{profile_id}/rules", authn(auth.RequirePermission("routing_profiles:update")(stdhttp.HandlerFunc(routingProfilesHandler.CreateRule))))
	mux.Handle("PATCH /api/v1/routing-profiles/{profile_id}/rules/{rule_id}", authn(auth.RequirePermission("routing_profiles:update")(stdhttp.HandlerFunc(routingProfilesHandler.UpdateRule))))
	mux.Handle("DELETE /api/v1/routing-profiles/{profile_id}/rules/{rule_id}", authn(auth.RequirePermission("routing_profiles:update")(stdhttp.HandlerFunc(routingProfilesHandler.DeleteRule))))

	mux.Handle("GET /api/v1/node-groups", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(nodeGroupsHandler.List))))
	mux.Handle("POST /api/v1/node-groups", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(nodeGroupsHandler.Create))))
	mux.Handle("GET /api/v1/node-groups/{group_id}", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(nodeGroupsHandler.Get))))
	mux.Handle("PATCH /api/v1/node-groups/{group_id}", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(nodeGroupsHandler.Update))))
	mux.Handle("DELETE /api/v1/node-groups/{group_id}", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(nodeGroupsHandler.Delete))))
	mux.Handle("PUT /api/v1/node-groups/{group_id}/members/{server_id}", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(nodeGroupsHandler.PutMember))))
	mux.Handle("DELETE /api/v1/node-groups/{group_id}/members/{server_id}", authn(auth.RequirePermission("servers:update")(stdhttp.HandlerFunc(nodeGroupsHandler.DeleteMember))))
	mux.Handle("GET /api/v1/node-groups/{group_id}/candidates", authn(auth.RequirePermission("servers:read")(stdhttp.HandlerFunc(nodeGroupsHandler.Candidates))))

	mux.Handle("GET /api/v1/agents", authn(auth.RequirePermission("agents:read")(stdhttp.HandlerFunc(agentsHandler.List))))
	mux.Handle("GET /api/v1/dashboard/activity", adminAuth(dashboardHandler.Activity))
	mux.Handle("GET /api/v1/dashboard/traffic", adminAuth(dashboardHandler.Traffic))
	mux.Handle("GET /api/v1/dashboard/nodes", adminAuth(dashboardHandler.Nodes))
	mux.Handle("GET /api/v1/connections", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(connectionsHandler.List))))

	mux.Handle("GET /api/v1/vpn-accounts", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.List))))
	mux.Handle("POST /api/v1/vpn-accounts", authn(auth.RequirePermission("vpn_users:create")(stdhttp.HandlerFunc(vpnAccountsHandler.Create))))
	mux.Handle("POST /api/v1/vpn-accounts/bulk-update", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.BulkUpdate))))
	mux.Handle("POST /api/v1/vpn-accounts/bulk-disable", authn(auth.RequirePermission("vpn_users:disable")(stdhttp.HandlerFunc(vpnAccountsHandler.BulkDisable))))
	mux.Handle("GET /api/v1/vpn-accounts/access-summary", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.GetClientAccessSummary))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.Get))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/notes", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountNotesHandler.Get))))
	mux.Handle("PATCH /api/v1/vpn-accounts/{id}/notes", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountNotesHandler.Update))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/credentials", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.GetCredentials))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/client-connection", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.GetClientConnection))))
	// Unapplied credential previews require an explicit operator POST with
	// permissions for both VPN account management and node config inspection.
	mux.Handle("POST /api/v1/vpn-accounts/{id}/client-connection/pre-import", authn(auth.RequirePermission("vpn_users:update")(auth.RequirePermission("configs:read")(stdhttp.HandlerFunc(vpnAccountsHandler.PreviewUnappliedVLESS)))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/client-profile", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.GetClientProfile))))
	mux.Handle("PATCH /api/v1/vpn-accounts/{id}/client-profile", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.UpdateClientProfile))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/transfer", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(transfersHandler.Latest))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/transfer", authn(auth.RequirePermission("vpn_users:update")(auth.RequirePermission("configs:apply")(auth.RequirePermission("configs:render")(auth.RequirePermission("configs:validate")(stdhttp.HandlerFunc(transfersHandler.Start)))))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/transfer/{transferId}", authn(auth.RequirePermission("vpn_users:update")(auth.RequirePermission("configs:apply")(auth.RequirePermission("configs:render")(auth.RequirePermission("configs:validate")(stdhttp.HandlerFunc(transfersHandler.Act)))))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/routing-policy", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.GetRoutingPolicy))))
	mux.Handle("PUT /api/v1/vpn-accounts/{id}/routing-profile", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.AssignRoutingProfile))))
	mux.Handle("DELETE /api/v1/vpn-accounts/{id}/routing-profile", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.DeleteRoutingProfileAssignment))))
	mux.Handle("PUT /api/v1/vpn-accounts/{id}/node-group", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.AssignNodeGroup))))
	mux.Handle("DELETE /api/v1/vpn-accounts/{id}/node-group", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.DeleteNodeGroupAssignment))))
	mux.Handle("PUT /api/v1/vpn-accounts/{id}/automatic-selection", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.UpdateAutomaticSelectionPolicy))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/automatic-selection/preview", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.PreviewAutomaticSelection))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/automatic-selection/apply", authn(auth.RequirePermission("vpn_users:update")(auth.RequirePermission("configs:apply")(auth.RequirePermission("configs:render")(auth.RequirePermission("configs:validate")(stdhttp.HandlerFunc(vpnAccountsHandler.ApplyAutomaticSelection)))))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/traffic", authn(auth.RequirePermission("traffic:read")(stdhttp.HandlerFunc(trafficHandler.GetAccountUsage))))
	mux.Handle("PATCH /api/v1/vpn-accounts/{id}/traffic-limit", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(trafficHandler.UpdateAccountLimit))))
	mux.Handle("PATCH /api/v1/vpn-accounts/{id}", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.Update))))
	mux.Handle("DELETE /api/v1/vpn-accounts/{id}", authn(auth.RequirePermission("vpn_users:disable")(stdhttp.HandlerFunc(vpnAccountsHandler.Delete))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/suspend", authn(auth.RequirePermission("vpn_users:disable")(stdhttp.HandlerFunc(vpnAccountsHandler.Suspend))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/activate", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.Activate))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/revoke", authn(auth.RequirePermission("vpn_users:disable")(stdhttp.HandlerFunc(vpnAccountsHandler.Revoke))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/subscription-token", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.GetLegacySubscriptionAccess))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/subscription-token", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.CreateSubscriptionToken))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/subscription-token/rotate", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.RotateSubscriptionToken))))
	mux.Handle("DELETE /api/v1/vpn-accounts/{id}/subscription-token", authn(auth.RequirePermission("vpn_users:disable")(stdhttp.HandlerFunc(vpnAccountsHandler.RevokeSubscriptionToken))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/qr", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.GetSubscriptionQRCode))))
	mux.Handle("GET /api/v1/vpn-accounts/{id}/devices", authn(auth.RequirePermission("vpn_users:read")(stdhttp.HandlerFunc(vpnAccountsHandler.ListDevices))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/devices", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.CreateDevice))))
	mux.Handle("PATCH /api/v1/vpn-accounts/{id}/devices/{deviceId}", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.UpdateDevice))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/devices/{deviceId}/rotate", authn(auth.RequirePermission("vpn_users:update")(stdhttp.HandlerFunc(vpnAccountsHandler.RotateDeviceSubscriptionToken))))
	mux.Handle("POST /api/v1/vpn-accounts/{id}/devices/{deviceId}/revoke", authn(auth.RequirePermission("vpn_users:disable")(stdhttp.HandlerFunc(vpnAccountsHandler.RevokeDevice))))
	mux.HandleFunc("GET /api/v1/subscriptions/{token}", vpnAccountsHandler.GetPublicSubscription)
	mux.HandleFunc("GET /sub/{token}", vpnAccountsHandler.GetClientSubscription)

	mux.Handle("GET /api/v1/users", authn(auth.RequirePermission("users:read")(stdhttp.HandlerFunc(usersHandler.List))))
	mux.Handle("GET /api/v1/users/{id}", authn(auth.RequirePermission("users:read")(stdhttp.HandlerFunc(usersHandler.Get))))
	mux.Handle("POST /api/v1/users", authn(auth.RequirePermission("users:create")(stdhttp.HandlerFunc(usersHandler.Create))))
	mux.Handle("PATCH /api/v1/users/{id}", authn(auth.RequirePermission("users:update")(stdhttp.HandlerFunc(usersHandler.Update))))
	mux.Handle("POST /api/v1/users/{id}/disable", authn(auth.RequirePermission("users:disable")(stdhttp.HandlerFunc(usersHandler.Disable))))
	mux.Handle("POST /api/v1/users/{id}/enable", authn(auth.RequirePermission("users:disable")(stdhttp.HandlerFunc(usersHandler.Enable))))
	mux.Handle("GET /api/v1/roles", authn(auth.RequirePermission("roles:read")(stdhttp.HandlerFunc(rolesHandler.ListRoles))))
	mux.Handle("GET /api/v1/permissions", authn(auth.RequirePermission("roles:read")(stdhttp.HandlerFunc(rolesHandler.ListPermissions))))
	mux.Handle("GET /api/v1/system/version", authn(auth.RequirePermission("agents:read")(stdhttp.HandlerFunc(systemHandler.Version))))
	mux.Handle("GET /api/v1/system/maintenance", authn(auth.RequirePermission("system:manage")(stdhttp.HandlerFunc(maintenanceHandler.Inventory))))
	mux.Handle("POST /api/v1/system/maintenance/plans", authn(auth.RequirePermission("system:manage")(stdhttp.HandlerFunc(maintenanceHandler.CreatePlan))))
	mux.Handle("GET /api/v1/system/maintenance/plans/{plan_id}", authn(auth.RequirePermission("system:manage")(stdhttp.HandlerFunc(maintenanceHandler.GetPlan))))
	mux.Handle("POST /api/v1/system/maintenance/plans/{plan_id}/execute", authn(auth.RequirePermission("system:manage")(stdhttp.HandlerFunc(maintenanceHandler.Execute))))
	mux.HandleFunc("POST /api/v1/agent/register", agentsHandler.Register)
	mux.HandleFunc("POST /api/v1/agent/heartbeat", agentsHandler.Heartbeat)
	mux.HandleFunc("GET /api/v1/agent/tasks/next", agentsHandler.NextTask)
	mux.HandleFunc("POST /api/v1/agent/tasks/{job_id}/result", agentsHandler.CompleteTask)
	mux.HandleFunc("POST /api/v1/agent/tasks/{job_id}/result/verify", agentsHandler.VerifyTaskResult)
	mux.HandleFunc("POST /api/v1/agent/traffic-usage", trafficHandler.ReportUsage)
	mux.HandleFunc("POST /api/v1/agent/traffic-batches", trafficHandler.ReportUsage)
	mux.HandleFunc("POST /api/v1/agent/traffic-reports", trafficHandler.ReportUsage)
	mux.HandleFunc("POST /api/v1/agent/client-presence", connectionsHandler.ReportSnapshot)

	return chain(
		mux,
		recoverMiddleware(logger),
		requestIDMiddleware(),
		corsMiddleware(),
		loggingMiddleware(logger),
	)
}
