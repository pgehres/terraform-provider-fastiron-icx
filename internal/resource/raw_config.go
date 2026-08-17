package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/providerdata"
	"github.com/pgehres/terraform-provider-fastiron-icx/internal/sshclient"
)

var _ resource.Resource = &RawConfigResource{}

// contextEnteringKeywords are FastIron prefixes that enter a sub-context.
// Prepending "no" to these erases the entire sub-context rather than reversing
// a specific setting — so auto-"no" destroy is dangerous without explicit
// destroy_commands.
var contextEnteringKeywords = []string{"interface ", "vlan ", "router ", "aaa "}

type RawConfigResource struct {
	client sshclient.CommandExecutor
}

type RawConfigResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Commands        types.List   `tfsdk:"commands"`
	DestroyCommands types.List   `tfsdk:"destroy_commands"`
	ExpectInConfig  types.List   `tfsdk:"expect_in_config"`
}

func NewRawConfigResource() resource.Resource {
	return &RawConfigResource{}
}

func (r *RawConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_raw_config"
}

func (r *RawConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	// noNewlineValidator rejects CR/LF in list elements — one CLI command per
	// element; embedded newlines would inject additional commands into the session.
	noNewlineValidator := listvalidator.ValueStringsAre(
		stringvalidator.RegexMatches(
			reNoNewlines,
			"each element must be a single CLI command and must not contain carriage returns or newlines",
		),
	)

	resp.Schema = schema.Schema{
		Description: "Manages arbitrary CLI configuration lines on an ICX switch. Use this for features not covered by specific resources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"commands": schema.ListAttribute{
				Description: "CLI commands to execute in config mode on create/update.",
				Required:    true,
				ElementType: types.StringType,
				Validators:  []validator.List{noNewlineValidator},
			},
			"destroy_commands": schema.ListAttribute{
				Description: "CLI commands to execute in config mode on destroy. If not specified, each command from 'commands' is prefixed with 'no'.",
				Optional:    true,
				ElementType: types.StringType,
				Validators:  []validator.List{noNewlineValidator},
			},
			"expect_in_config": schema.ListAttribute{
				Description: "Lines expected to appear verbatim in the running config (trimmed line equality). If any are missing, Terraform detects drift and will re-apply.",
				Optional:    true,
				ElementType: types.StringType,
				Validators:  []validator.List{noNewlineValidator},
			},
		},
	}
}

func (r *RawConfigResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerdata.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *providerdata.ProviderData, got %T", req.ProviderData))
		return
	}
	r.client = data.Client
}

func (r *RawConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RawConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	commands := listToStringSlice(ctx, plan.Commands, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// Warn when destroy_commands is unset and any command enters a sub-context.
	// On destroy the provider would prepend "no", erasing the entire context
	// (e.g., "no interface ethernet 1/1/1" deletes all port config on FastIron).
	destroyCmds := listToStringSlice(ctx, plan.DestroyCommands, &resp.Diagnostics)
	if len(destroyCmds) == 0 {
		for _, cmd := range commands {
			for _, kw := range contextEnteringKeywords {
				if strings.HasPrefix(cmd, kw) {
					resp.Diagnostics.AddWarning(
						"Auto-'no' destroy will erase the entire sub-context",
						fmt.Sprintf(
							"Command %q starts with context-entering keyword %q. Without destroy_commands set, "+
								"destroy will run %q, which erases the entire %s sub-context on FastIron (not just "+
								"the specific setting). Set destroy_commands explicitly to avoid unintended data loss.",
							cmd, kw, "no "+cmd, strings.TrimSpace(kw),
						),
					)
					break
				}
			}
		}
	}

	if len(commands) > 0 {
		if err := r.client.ExecuteInConfigMode(commands); err != nil {
			resp.Diagnostics.AddError("Failed to apply raw config", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}

	// ID is a SHA-256 digest of all commands joined with newlines, truncated to
	// 12 hex chars. This avoids the collision that the previous first-40-chars
	// scheme had when two resources shared a common command prefix.
	// Pre-existing state keeps its old ID harmlessly — the ID is an opaque
	// identifier; Read and Delete do not parse or match on it.
	plan.ID = types.StringValue(hashCommands(commands))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RawConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RawConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	expectLines := listToStringSlice(ctx, state.ExpectInConfig, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	// If no expect_in_config is set, we can't verify drift — just trust state.
	if len(expectLines) == 0 {
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}

	// Check running config for expected lines using trimmed line equality.
	// strings.Contains was replaced with a line-by-line match to prevent false
	// positives where the expected string is a substring of an unrelated line.
	runningConfig, err := r.client.GetRunningConfig()
	if err != nil {
		resp.Diagnostics.AddError("Failed to read running config", err.Error())
		return
	}

	configLines := strings.Split(runningConfig, "\n")

	for _, expected := range expectLines {
		trimmedExpected := strings.TrimSpace(expected)
		found := false
		for _, line := range configLines {
			if strings.TrimSpace(line) == trimmedExpected {
				found = true
				break
			}
		}
		if !found {
			// Expected line is missing — remove from state to trigger re-apply.
			resp.State.RemoveResource(ctx)
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *RawConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RawConfigResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Remove old commands first.
	oldCommands := listToStringSlice(ctx, state.Commands, &resp.Diagnostics)
	oldDestroy := listToStringSlice(ctx, state.DestroyCommands, &resp.Diagnostics)

	var removeCommands []string
	if len(oldDestroy) > 0 {
		removeCommands = oldDestroy
	} else {
		for _, cmd := range oldCommands {
			removeCommands = append(removeCommands, "no "+cmd)
		}
	}

	if len(removeCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(removeCommands); err != nil {
			resp.Diagnostics.AddError("Failed to remove old raw config", err.Error())
			return
		}
	}

	// Apply new commands.
	newCommands := listToStringSlice(ctx, plan.Commands, &resp.Diagnostics)
	if len(newCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(newCommands); err != nil {
			resp.Diagnostics.AddError("Failed to apply raw config", err.Error())
			return
		}
	}

	if err := r.client.WriteMemory(); err != nil {
		resp.Diagnostics.AddError("Failed to save configuration", err.Error())
		return
	}

	plan.ID = types.StringValue(hashCommands(newCommands))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RawConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RawConfigResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	destroyCommands := listToStringSlice(ctx, state.DestroyCommands, &resp.Diagnostics)
	if len(destroyCommands) == 0 {
		// Auto-generate destroy commands by prefixing with "no".
		commands := listToStringSlice(ctx, state.Commands, &resp.Diagnostics)
		for _, cmd := range commands {
			destroyCommands = append(destroyCommands, "no "+cmd)
		}
	}

	if len(destroyCommands) > 0 {
		if err := r.client.ExecuteInConfigMode(destroyCommands); err != nil {
			resp.Diagnostics.AddError("Failed to remove raw config", err.Error())
			return
		}
		if err := r.client.WriteMemory(); err != nil {
			resp.Diagnostics.AddError("Failed to save configuration", err.Error())
			return
		}
	}
}

// hashCommands returns a stable, collision-resistant ID for a set of commands.
// It computes SHA-256 over all commands joined with newlines and returns the
// first 12 hex characters prefixed with "rawcfg-". Using all commands (rather
// than just the first one) prevents ID collisions when two resources share a
// common command prefix.
func hashCommands(commands []string) string {
	if len(commands) == 0 {
		return "rawcfg-empty"
	}
	joined := strings.Join(commands, "\n")
	sum := sha256.Sum256([]byte(joined))
	return "rawcfg-" + hex.EncodeToString(sum[:])[:12]
}
