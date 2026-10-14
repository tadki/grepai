Feature: SEE-1411 §SPEC-001/002 — rpg CLI 的 workspace RPG 通路与 cwd 自动归属

  Scenario: §SPEC-001 显式 workspace 搜索命中 godot-qa-toolkit 的 gherkin 节点
    Given a built grepai binary
    When rpg search runs with query "gherkin" and --workspace gqt --json
    Then at least one result node path contains "gherkin"
    And every returned node was updated within the last day

  Scenario: §SPEC-001 fetch 已知节点返回层级与边
    Given a built grepai binary with a known gherkin node id from search
    When rpg fetch runs on that node id with --workspace gqt --json
    Then the fetched node id equals the query id
    And the fetch result carries parents and feature path

  Scenario: §SPEC-001 explore 双向遍历返回邻域
    Given a built grepai binary with a known gherkin node id from search
    When rpg explore runs on that node id with --workspace gqt --json
    Then the explored graph contains the start node
    And the explored graph has more than one node

  Scenario: §SPEC-001 非法 workspace 报干净错误
    Given a built grepai binary
    When rpg search runs with query "anything" and --workspace ghost --json
    Then the command fails with error text mentioning "ghost"

  Scenario: §SPEC-001 --workspace 下非项目报干净错误，且 --project 离不开 --workspace
    Given a built grepai binary
    When rpg search runs with query "anything" and --workspace gqt --project phantom --json
    Then the command fails with error text mentioning "phantom"
    When rpg search runs with query "anything" and --project gqt --json
    Then the command fails with a --project-requires-workspace error

  Scenario: §SPEC-002 GQT 仓内裸 search 自动归属读 workspace 图
    Given the godot-qa-toolkit project cwd of the gqt workspace
    When a bare rpg search runs with query "gherkin" and --json
    Then results are non-empty with no error
    And every returned node was updated within the last day

  Scenario: §SPEC-002 KOL 仓内裸 search 返回 postgres 新鲜图而非陈旧本地图
    Given the king-of-likes project cwd of the kol workspace
    When a bare rpg search runs with query "add_tag" and --json
    Then results are non-empty with no error
    And every returned node was updated within the last day

  Scenario: §SPEC-004 只读查询不改写项目 .grepai gob 文件
    Given the godot-qa-toolkit project cwd of the gqt workspace
    When a bare rpg search runs with query "gherkin" and --json
    Then no .grepai gob file under the project changed since the scenario start
