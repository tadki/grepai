package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/alpkeskin/gotoon"
	"github.com/spf13/cobra"
	"github.com/yoanbernabeu/grepai/rpg"
)

var (
	rpgJSON             bool
	rpgTOON             bool
	rpgSearchScope      string
	rpgSearchKinds      string
	rpgSearchLimit      int
	rpgExploreDirection string
	rpgExploreDepth     int
	rpgExploreEdgeTypes string
	rpgExploreLimit     int
	rpgWorkspace        string
	rpgProject          string
)

var rpgCmd = &cobra.Command{
	Use:   "rpg <subcommand>",
	Short: "Search and explore the Repository Planning Graph",
	Long: `Search and explore the Repository Planning Graph (RPG).

The RPG maps code entities to feature hierarchy nodes and dependency edges.
Use these commands when you need semantic feature context beyond vector search.`,
}

var rpgSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search RPG nodes by feature semantics",
	Args:  cobra.ExactArgs(1),
	RunE:  runRPGSearch,
}

var rpgFetchCmd = &cobra.Command{
	Use:   "fetch <node-id>",
	Short: "Fetch hierarchy and edge context for an RPG node",
	Args:  cobra.ExactArgs(1),
	RunE:  runRPGFetch,
}

var rpgExploreCmd = &cobra.Command{
	Use:   "explore <start-node-id>",
	Short: "Traverse an RPG graph neighborhood",
	Args:  cobra.ExactArgs(1),
	RunE:  runRPGExplore,
}

func init() {
	for _, cmd := range []*cobra.Command{rpgSearchCmd, rpgFetchCmd, rpgExploreCmd} {
		cmd.Flags().BoolVar(&rpgJSON, "json", false, "Output results in JSON format")
		cmd.Flags().BoolVarP(&rpgTOON, "toon", "t", false, "Output results in TOON format (token-efficient for AI agents)")
		cmd.MarkFlagsMutuallyExclusive("json", "toon")
		cmd.Flags().StringVar(&rpgWorkspace, "workspace", "", "Workspace name for cross-project rpg")
		cmd.Flags().StringVar(&rpgProject, "project", "", "Project name within workspace (requires --workspace)")
	}

	rpgSearchCmd.Flags().StringVar(&rpgSearchScope, "scope", "", "Area/category path to narrow search, such as 'cli' or 'rpg/query'")
	rpgSearchCmd.Flags().StringVar(&rpgSearchKinds, "kinds", "", "Comma-separated node kinds: area, category, subcategory, file, symbol, chunk")
	rpgSearchCmd.Flags().IntVarP(&rpgSearchLimit, "limit", "n", 10, "Maximum number of results to return")

	rpgExploreCmd.Flags().StringVar(&rpgExploreDirection, "direction", "both", "Traversal direction: forward, reverse, or both")
	rpgExploreCmd.Flags().IntVarP(&rpgExploreDepth, "depth", "d", 2, "Maximum BFS traversal depth")
	rpgExploreCmd.Flags().StringVar(&rpgExploreEdgeTypes, "edge-types", "", "Comma-separated edge types: feature_parent, contains, invokes, imports, maps_to_chunk, semantic_sim")
	rpgExploreCmd.Flags().IntVarP(&rpgExploreLimit, "limit", "n", 100, "Maximum nodes to return")

	rpgCmd.AddCommand(rpgSearchCmd)
	rpgCmd.AddCommand(rpgFetchCmd)
	rpgCmd.AddCommand(rpgExploreCmd)
	rootCmd.AddCommand(rpgCmd)
}

func parseRPGNodeKinds(kindsStr string) ([]rpg.NodeKind, error) {
	if strings.TrimSpace(kindsStr) == "" {
		return nil, nil
	}
	parts := strings.Split(kindsStr, ",")
	kinds := make([]rpg.NodeKind, 0, len(parts))
	for _, part := range parts {
		kind := strings.TrimSpace(part)
		switch kind {
		case "area":
			kinds = append(kinds, rpg.KindArea)
		case "category":
			kinds = append(kinds, rpg.KindCategory)
		case "subcategory":
			kinds = append(kinds, rpg.KindSubcategory)
		case "file":
			kinds = append(kinds, rpg.KindFile)
		case "symbol":
			kinds = append(kinds, rpg.KindSymbol)
		case "chunk":
			kinds = append(kinds, rpg.KindChunk)
		case "":
			continue
		default:
			return nil, fmt.Errorf("invalid kind: %s", kind)
		}
	}
	return kinds, nil
}

func parseRPGEdgeTypes(edgeTypesStr string) ([]rpg.EdgeType, error) {
	if strings.TrimSpace(edgeTypesStr) == "" {
		return nil, nil
	}
	parts := strings.Split(edgeTypesStr, ",")
	edgeTypes := make([]rpg.EdgeType, 0, len(parts))
	for _, part := range parts {
		edgeType := strings.TrimSpace(part)
		switch edgeType {
		case "feature_parent":
			edgeTypes = append(edgeTypes, rpg.EdgeFeatureParent)
		case "contains":
			edgeTypes = append(edgeTypes, rpg.EdgeContains)
		case "invokes":
			edgeTypes = append(edgeTypes, rpg.EdgeInvokes)
		case "imports":
			edgeTypes = append(edgeTypes, rpg.EdgeImports)
		case "maps_to_chunk":
			edgeTypes = append(edgeTypes, rpg.EdgeMapsToChunk)
		case "semantic_sim":
			edgeTypes = append(edgeTypes, rpg.EdgeSemanticSim)
		case "":
			continue
		default:
			return nil, fmt.Errorf("invalid edge type: %s", edgeType)
		}
	}
	return edgeTypes, nil
}

func validateRPGWorkspaceFlags() error {
	if rpgProject != "" && rpgWorkspace == "" {
		return fmt.Errorf("--project requires --workspace")
	}
	return nil
}

// loadRPGStore resolves the RPG store through the shared dual-mode loader:
// explicit --workspace reads the workspace scope, otherwise cwd attribution
// picks the shared (postgres) store or the local single-project gob.
func loadRPGStore(ctx context.Context) (rpg.RPGStore, *rpg.QueryEngine, error) {
	store, qe, err := rpg.LoadRPGForScope(ctx, rpgWorkspace, rpgProject)
	if errors.Is(err, rpg.ErrRPGIndexOutdated) {
		return nil, nil, fmt.Errorf("RPG index is outdated; run 'grepai watch' to rebuild")
	}
	if err != nil {
		return nil, nil, err
	}
	return store, qe, nil
}

func validateRPGDirection(direction string) error {
	if direction != "forward" && direction != "reverse" && direction != "both" {
		return fmt.Errorf("direction must be 'forward', 'reverse', or 'both'")
	}
	return nil
}

func outputRPGStructured(data any) error {
	if rpgJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	}
	if rpgTOON {
		output, err := gotoon.Encode(data)
		if err != nil {
			return fmt.Errorf("failed to encode TOON: %w", err)
		}
		fmt.Println(output)
	}
	return nil
}

func runRPGSearch(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := validateRPGWorkspaceFlags(); err != nil {
		return err
	}
	kinds, err := parseRPGNodeKinds(rpgSearchKinds)
	if err != nil {
		return err
	}
	store, qe, err := loadRPGStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	results, err := qe.SearchNode(ctx, rpg.SearchNodeRequest{
		Query: args[0],
		Scope: rpgSearchScope,
		Kinds: kinds,
		Limit: rpgSearchLimit,
	})
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}
	if rpgJSON || rpgTOON {
		return outputRPGStructured(results)
	}
	return displayRPGSearchResults(results)
}

func runRPGFetch(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := validateRPGWorkspaceFlags(); err != nil {
		return err
	}
	store, qe, err := loadRPGStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := qe.FetchNode(ctx, rpg.FetchNodeRequest{NodeID: args[0]})
	if err != nil {
		return fmt.Errorf("fetch failed: %w", err)
	}
	if result == nil {
		return fmt.Errorf("node not found: %s", args[0])
	}
	if rpgJSON || rpgTOON {
		return outputRPGStructured(result)
	}
	return displayRPGFetchResult(result)
}

func displayRPGFetchResult(result *rpg.FetchNodeResult) error {
	node := result.Node
	fmt.Printf("Node: %s\n", node.ID)
	fmt.Printf("Kind: %s\n", node.Kind)
	if node.SymbolName != "" {
		fmt.Printf("Symbol: %s\n", node.SymbolName)
	}
	if node.Feature != "" {
		fmt.Printf("Feature: %s\n", node.Feature)
	}
	if result.FeaturePath != "" {
		fmt.Printf("Feature path: %s\n", result.FeaturePath)
	}
	if node.Path != "" {
		fmt.Printf("Path: %s", node.Path)
		if node.StartLine > 0 {
			fmt.Printf(":%d", node.StartLine)
		}
		fmt.Println()
	}
	fmt.Printf("Parents: %d\n", len(result.Parents))
	fmt.Printf("Children: %d\n", len(result.Children))
	fmt.Printf("Incoming edges: %d\n", len(result.Incoming))
	fmt.Printf("Outgoing edges: %d\n", len(result.Outgoing))
	if result.CodePreview != "" {
		fmt.Println("\nCode preview:")
		fmt.Println(result.CodePreview)
	}
	return nil
}

func runRPGExplore(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := validateRPGWorkspaceFlags(); err != nil {
		return err
	}
	if err := validateRPGDirection(rpgExploreDirection); err != nil {
		return err
	}
	edgeTypes, err := parseRPGEdgeTypes(rpgExploreEdgeTypes)
	if err != nil {
		return err
	}
	store, qe, err := loadRPGStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	result, err := qe.Explore(ctx, rpg.ExploreRequest{
		StartNodeID: args[0],
		Direction:   rpgExploreDirection,
		Depth:       rpgExploreDepth,
		EdgeTypes:   edgeTypes,
		Limit:       rpgExploreLimit,
	})
	if err != nil {
		return fmt.Errorf("explore failed: %w", err)
	}
	if result == nil {
		return fmt.Errorf("start node not found: %s", args[0])
	}
	if rpgJSON || rpgTOON {
		return outputRPGStructured(result)
	}
	return displayRPGExploreResult(result)
}

func displayRPGExploreResult(result *rpg.ExploreResult) error {
	fmt.Printf("Start: %s\n", result.StartNode.ID)
	fmt.Printf("Depth reached: %d\n", result.Depth)
	fmt.Printf("Nodes: %d\n", len(result.Nodes))
	fmt.Printf("Edges: %d\n", len(result.Edges))
	fmt.Println(strings.Repeat("-", 60))

	ids := make([]string, 0, len(result.Nodes))
	for id := range result.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		node := result.Nodes[id]
		label := node.Feature
		if node.SymbolName != "" {
			label = node.SymbolName
		}
		fmt.Printf("%s (%s) %s\n", node.ID, node.Kind, label)
	}
	return nil
}

func displayRPGSearchResults(results []rpg.SearchNodeResult) error {
	fmt.Printf("RPG Results (%d):\n", len(results))
	fmt.Println(strings.Repeat("-", 60))
	if len(results) == 0 {
		fmt.Println("No RPG nodes found.")
		return nil
	}
	for i, result := range results {
		node := result.Node
		name := node.Feature
		if node.SymbolName != "" {
			name = node.SymbolName
		}
		fmt.Printf("\n%d. %s (%s) score %.3f\n", i+1, name, node.Kind, result.Score)
		fmt.Printf("   Node: %s\n", node.ID)
		if result.FeaturePath != "" {
			fmt.Printf("   Feature path: %s\n", result.FeaturePath)
		}
		if node.Path != "" {
			fmt.Printf("   Path: %s", node.Path)
			if node.StartLine > 0 {
				fmt.Printf(":%d", node.StartLine)
			}
			fmt.Println()
		}
	}
	return nil
}
