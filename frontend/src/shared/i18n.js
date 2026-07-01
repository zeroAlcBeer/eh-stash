// Tiny module-level i18n. Locale is detected once at load — there's no
// language switcher, so no need for context / re-renders.
//
// Resolution order:
//   navigator.languages[*] → zh-TW (Hant/HK/TW) | zh-CN (other zh) | en

function detectLocale() {
  const langs =
    typeof navigator !== 'undefined'
      ? (navigator.languages?.length ? navigator.languages : [navigator.language])
      : [];
  for (const l of langs) {
    if (!l) continue;
    const low = l.toLowerCase();
    if (low.startsWith('zh-tw') || low.startsWith('zh-hk') || low.includes('hant')) {
      return 'zh-TW';
    }
    if (low.startsWith('zh')) return 'zh-CN';
    if (low.startsWith('en')) return 'en';
  }
  return 'en';
}

export const locale = detectLocale();

const INTL_LOCALE = { 'zh-CN': 'zh-CN', 'zh-TW': 'zh-TW', en: 'en-US' };

const T = {
  // Site / nav
  'site.title': { 'zh-CN': 'EhStash', 'zh-TW': 'EhStash', en: 'EhStash' },

  // 404 / errors
  'notFound.subtitle': { 'zh-CN': '页面不存在', 'zh-TW': '頁面不存在', en: 'Page not found' },
  'notFound.back': { 'zh-CN': '返回首页', 'zh-TW': '返回首頁', en: 'Back to home' },
  'error.title': { 'zh-CN': '页面出错了', 'zh-TW': '頁面出錯了', en: 'Something went wrong' },
  'error.reload': { 'zh-CN': '刷新页面', 'zh-TW': '重新整理頁面', en: 'Reload' },

  // Loading / status
  'loading': { 'zh-CN': '加载中…', 'zh-TW': '載入中…', en: 'Loading…' },

  // Filter panel
  'filter.title': { 'zh-CN': '筛选', 'zh-TW': '篩選', en: 'Filters' },
  'filter.category': { 'zh-CN': '分类', 'zh-TW': '分類', en: 'Category' },
  'filter.minFav': { 'zh-CN': '最少收藏', 'zh-TW': '最少收藏', en: 'Min Fav' },
  'filter.tag': { 'zh-CN': '标签', 'zh-TW': '標籤', en: 'Tag' },
  'filter.all': { 'zh-CN': '全部', 'zh-TW': '全部', en: 'All' },
  'filter.reset': { 'zh-CN': '重置', 'zh-TW': '重設', en: 'Reset' },
  'filter.removeTag': { 'zh-CN': '移除标签 {tag}', 'zh-TW': '移除標籤 {tag}', en: 'Remove tag {tag}' },
  'filter.clearSearch': { 'zh-CN': '清除搜索', 'zh-TW': '清除搜尋', en: 'Clear search' },
  'filter.noMatch': { 'zh-CN': '无匹配标签', 'zh-TW': '無相符標籤', en: 'No matching tag' },

  // Sort / view controls
  'sort.fav': { 'zh-CN': '收藏', 'zh-TW': '收藏', en: 'Fav' },
  'sort.rating': { 'zh-CN': '评分', 'zh-TW': '評分', en: 'Rating' },
  'sort.comments': { 'zh-CN': '评论', 'zh-TW': '評論', en: 'Comments' },
  'sort.date': { 'zh-CN': '日期', 'zh-TW': '日期', en: 'Date' },
  'sort.label': { 'zh-CN': '排序方式：{label}', 'zh-TW': '排序方式：{label}', en: 'Sort: {label}' },
  'sort.menu': { 'zh-CN': '排序选项', 'zh-TW': '排序選項', en: 'Sort options' },
  'rating.any': { 'zh-CN': '不限', 'zh-TW': '不限', en: 'Any' },
  'rating.min': { 'zh-CN': '最低评分 {value}', 'zh-TW': '最低評分 {value}', en: 'Min rating {value}' },
  'rating.filterAll': { 'zh-CN': '评分筛选：全部', 'zh-TW': '評分篩選：全部', en: 'Rating: any' },
  'view.toList': { 'zh-CN': '切换到列表视图', 'zh-TW': '切換到清單檢視', en: 'Switch to list view' },
  'view.toGrid': { 'zh-CN': '切换到网格视图', 'zh-TW': '切換到網格檢視', en: 'Switch to grid view' },
  'view.list': { 'zh-CN': '列表', 'zh-TW': '清單', en: 'List' },
  'view.grid': { 'zh-CN': '网格', 'zh-TW': '網格', en: 'Grid' },

  // Results bar
  'results.summary': {
    'zh-CN': '共 {total} 条 · 第 {page}/{pages} 页',
    'zh-TW': '共 {total} 條 · 第 {page}/{pages} 頁',
    en: '{total} results · page {page} / {pages}'
  },

  // Pagination
  'page.pagination': { 'zh-CN': '分页', 'zh-TW': '分頁', en: 'Pagination' },
  'page.first': { 'zh-CN': '第一页', 'zh-TW': '第一頁', en: 'First page' },
  'page.prev': { 'zh-CN': '上一页', 'zh-TW': '上一頁', en: 'Previous page' },
  'page.next': { 'zh-CN': '下一页', 'zh-TW': '下一頁', en: 'Next page' },
  'page.last': { 'zh-CN': '最后一页', 'zh-TW': '最後一頁', en: 'Last page' },
  'page.number': { 'zh-CN': '第 {page} 页', 'zh-TW': '第 {page} 頁', en: 'Page {page}' },

  // Card / group
  'card.openEx': { 'zh-CN': '在 ExHentai 打开', 'zh-TW': '在 ExHentai 開啟', en: 'Open in ExHentai' },
  'card.viewVersions': { 'zh-CN': '查看 {count} 个版本', 'zh-TW': '查看 {count} 個版本', en: 'View {count} versions' },
  'card.versionsLabel': { 'zh-CN': '{count} 版本', 'zh-TW': '{count} 版本', en: '{count} ver.' },
  'group.versions': { 'zh-CN': '{count} 个版本', 'zh-TW': '{count} 個版本', en: '{count} versions' },
  'group.close': { 'zh-CN': '关闭', 'zh-TW': '關閉', en: 'Close' },

  // Welcome modal — step 1: age gate
  'welcome.title': { 'zh-CN': '欢迎使用 EhStash', 'zh-TW': '歡迎使用 EhStash', en: 'Welcome to EhStash' },
  'welcome.age.heading': {
    'zh-CN': '请确认您已年满 18 岁',
    'zh-TW': '請確認您已年滿 18 歲',
    en: 'Please confirm you are 18 or older'
  },
  'welcome.age.body': {
    'zh-CN': '本站汇集 ExHentai 的公开元数据，内容可能包含成人材料。继续浏览前请确认您已年满 18 岁，并接受您所在地区允许此类内容的访问。',
    'zh-TW': '本站匯集 ExHentai 的公開中介資料，內容可能含有成人材料。繼續瀏覽前請確認您已年滿 18 歲，並接受您所在地區允許此類內容的存取。',
    en: 'This site aggregates public metadata from ExHentai and may include adult content. Before continuing, confirm you are 18+ and that adult content is permitted in your jurisdiction.'
  },
  'welcome.age.yes': { 'zh-CN': '是，我已年满 18 岁', 'zh-TW': '是，我已年滿 18 歲', en: "Yes, I'm 18+" },
  'welcome.age.no': { 'zh-CN': '否，未满 18 岁', 'zh-TW': '否，未滿 18 歲', en: 'No, I am not' },

  // Welcome modal — denied terminal screen (chose "No")
  'welcome.denied.title': { 'zh-CN': '您不符合访问条件', 'zh-TW': '您不符合存取條件', en: 'Access not permitted' },
  'welcome.denied.body': {
    'zh-CN': '本站内容仅面向已年满 18 岁的访问者。请关闭此页面。',
    'zh-TW': '本站內容僅供已年滿 18 歲的訪客瀏覽，請關閉此頁面。',
    en: 'This site is restricted to users aged 18 or older. Please close this page.'
  },

  // Welcome modal — step 2: EhStash vs ExHentai
  'welcome.compare.title': {
    'zh-CN': 'EhStash 与 ExHentai 有什么不同？',
    'zh-TW': 'EhStash 與 ExHentai 有什麼不同？',
    en: 'How does EhStash differ from ExHentai?'
  },
  'welcome.compare.subtitle': {
    'zh-CN': '开始浏览前请先了解这几点',
    'zh-TW': '開始瀏覽前請先了解這幾點',
    en: 'A few things to know before you start'
  },

  'welcome.compare.index.title': { 'zh-CN': '索引站，不是内容站', 'zh-TW': '索引站，不是內容站', en: 'Index, not a host' },
  'welcome.compare.index.body': {
    'zh-CN': 'EhStash 只保存元数据和封面缩略图。点击卡片会跳转到 ExHentai 阅读完整画廊。移动端推荐使用 EhViewer 关联链接以获得最佳体验。',
    'zh-TW': 'EhStash 只保存中介資料和封面縮圖。點擊卡片會跳轉到 ExHentai 閱讀完整畫廊。行動裝置推薦使用 EhViewer 關聯連結以獲得最佳體驗。',
    en: 'EhStash only stores metadata and cover thumbnails. Click a card to jump to ExHentai for the full gallery. Mobile users get the best experience by associating links with EhViewer.'
  },

  'welcome.compare.fav.title': { 'zh-CN': 'Fav 排序', 'zh-TW': 'Fav 排序', en: 'Fav-first sort' },
  'welcome.compare.fav.body': {
    'zh-CN': '翻页是按发布时间倒序的全量切片；每页内部默认按收藏数倒序，热门内容先看到。',
    'zh-TW': '翻頁是按發布時間遞減的全量切片；每頁內預設按收藏數遞減，熱門內容會先看到。',
    en: 'Pagination steps through all items newest-first; within each page items are sorted by favorites — popular ones come first.'
  },

  'welcome.compare.group.title': { 'zh-CN': 'Group 功能', 'zh-TW': 'Group 功能', en: 'Group collapse' },
  'welcome.compare.group.body': {
    'zh-CN': '同系列的画廊会聚合成一张卡片，点击后在弹窗里查看全部版本。',
    'zh-TW': '同系列的畫廊會聚合成一張卡片，點擊後可在彈窗中查看所有版本。',
    en: 'Galleries in the same series collapse into one card — click to see every version in the modal.'
  },

  'welcome.compare.continue': { 'zh-CN': '我明白了，开始浏览', 'zh-TW': '我明白了，開始瀏覽', en: 'Got it, start browsing' },

  // Settings menu
  'settings.title':              { 'zh-CN': '设置',                'zh-TW': '設定',                en: 'Settings' },
  'settings.open':               { 'zh-CN': '打开设置',            'zh-TW': '開啟設定',            en: 'Open settings' },
  'settings.allowCosplay.label':      { 'zh-CN': '我可以接受三次元画廊', 'zh-TW': '我可以接受三次元畫廊', en: 'Show live-action galleries' },
  'settings.allowCosplay.hint':       { 'zh-CN': '勾选后 Cosplay 分类与画廊会出现在列表中',
                                   'zh-TW': '勾選後 Cosplay 分類與畫廊會出現在清單中',
                                   en:      'Once enabled, Cosplay galleries appear in the listing.' },

  // Gallery page — self-hosted extras
  'results.filtered':        { 'zh-CN': '· 显示 {count} 条', 'zh-TW': '· 顯示 {count} 條', en: '· showing {count}' },
  'translation.on':          { 'zh-CN': '开启中文翻译', 'zh-TW': '開啟中文翻譯', en: 'Enable Chinese translation' },
  'translation.off':         { 'zh-CN': '关闭中文翻译', 'zh-TW': '關閉中文翻譯', en: 'Disable Chinese translation' },
  'translation.label':       { 'zh-CN': '中文', 'zh-TW': '中文', en: 'CN' },
  'recommended.empty':       { 'zh-CN': '还没有推荐内容', 'zh-TW': '還沒有推薦內容', en: 'No recommendations yet' },
  'recommended.empty.hint':  { 'zh-CN': '请先在 Admin 中创建并启动 Favorites Sync 任务', 'zh-TW': '請先在 Admin 中建立並啟動 Favorites Sync 任務', en: 'Create and start a Favorites Sync task in Admin first' },

  // Admin page
  'admin.subtitle':              { 'zh-CN': '任务调度 & 缩略图队列', 'zh-TW': '任務排程 & 縮圖佇列', en: 'Task scheduling & thumb queue' },
  'admin.newTask':               { 'zh-CN': '新建任务', 'zh-TW': '新建任務', en: 'New Task' },
  'admin.closeError':            { 'zh-CN': '关闭错误提示', 'zh-TW': '關閉錯誤提示', en: 'Close error' },
  'admin.loadError':             { 'zh-CN': '加载数据失败，请检查后端连接', 'zh-TW': '載入資料失敗，請檢查後端連線', en: 'Failed to load data, check backend connection' },
  'admin.operationFailed':       { 'zh-CN': '操作失败', 'zh-TW': '操作失敗', en: 'Operation failed' },

  // Admin — task row actions
  'admin.task.start':            { 'zh-CN': '启动任务', 'zh-TW': '啟動任務', en: 'Start task' },
  'admin.task.startKick':        { 'zh-CN': '启用并投递 kick', 'zh-TW': '啟用並投遞 kick', en: 'Enable & deliver kick' },
  'admin.task.startAria':        { 'zh-CN': '启动任务 {name}', 'zh-TW': '啟動任務 {name}', en: 'Start task {name}' },
  'admin.task.stop':             { 'zh-CN': '取消当前 job 或停用定义', 'zh-TW': '取消目前 job 或停用定義', en: 'Cancel current job or disable definition' },
  'admin.task.stopAria':         { 'zh-CN': '停止任务 {name}', 'zh-TW': '停止任務 {name}', en: 'Stop task {name}' },
  'admin.task.retry':            { 'zh-CN': '重试最近 job', 'zh-TW': '重試最近 job', en: 'Retry latest job' },
  'admin.task.retryAria':        { 'zh-CN': '重试任务 {name}', 'zh-TW': '重試任務 {name}', en: 'Retry task {name}' },
  'admin.task.delete':           { 'zh-CN': '删除任务定义', 'zh-TW': '刪除任務定義', en: 'Delete task definition' },
  'admin.task.deleteAria':       { 'zh-CN': '删除任务 {name}', 'zh-TW': '刪除任務 {name}', en: 'Delete task {name}' },
  'admin.task.expand':           { 'zh-CN': '展开详情', 'zh-TW': '展開詳情', en: 'Expand details' },
  'admin.task.collapse':         { 'zh-CN': '收起详情', 'zh-TW': '收起詳情', en: 'Collapse details' },
  'admin.task.expandAria':       { 'zh-CN': '展开任务 {name} 详情', 'zh-TW': '展開任務 {name} 詳情', en: 'Expand task {name} details' },
  'admin.task.collapseAria':     { 'zh-CN': '收起任务 {name} 详情', 'zh-TW': '收起任務 {name} 詳情', en: 'Collapse task {name} details' },

  // Admin — create task modal
  'admin.task.nameRequired':     { 'zh-CN': '名称不能为空', 'zh-TW': '名稱不能為空', en: 'Name is required' },
  'admin.task.oneIncremental':   { 'zh-CN': '仅允许创建一个 gallery incremental sync', 'zh-TW': '僅允許建立一個 gallery incremental sync', en: 'Only one gallery incremental sync is allowed' },
  'admin.task.oneFavorites':     { 'zh-CN': '仅允许创建一个 favorites source sync', 'zh-TW': '僅允許建立一個 favorites source sync', en: 'Only one favorites source sync is allowed' },
  'admin.task.minOneCategory':   { 'zh-CN': 'incremental 至少选择一个分类', 'zh-TW': 'incremental 至少選擇一個分類', en: 'Select at least one category for incremental' },
  'admin.task.createFailed':     { 'zh-CN': '创建任务失败', 'zh-TW': '建立任務失敗', en: 'Failed to create task' },
  'admin.task.createTitle':      { 'zh-CN': '新建同步任务', 'zh-TW': '新建同步任務', en: 'New Sync Task' },
  'admin.task.close':            { 'zh-CN': '关闭', 'zh-TW': '關閉', en: 'Close' },
  'admin.task.name':             { 'zh-CN': '任务名称', 'zh-TW': '任務名稱', en: 'Task name' },
  'admin.task.type':             { 'zh-CN': '类型', 'zh-TW': '類型', en: 'Type' },
  'admin.task.category':         { 'zh-CN': '分类 (Category)', 'zh-TW': '分類 (Category)', en: 'Category' },
  'admin.task.categories':       { 'zh-CN': '分类 (categories)', 'zh-TW': '分類 (categories)', en: 'Categories' },
  'admin.task.incrementalHint':  { 'zh-CN': 'Gallery incremental 使用 Mixed scope，按上传活跃度抓取。', 'zh-TW': 'Gallery incremental 使用 Mixed scope，按上傳活躍度抓取。', en: 'Gallery incremental uses Mixed scope, scraping by upload activity.' },
  'admin.task.startGid':         { 'zh-CN': 'start_gid (可选)', 'zh-TW': 'start_gid (可選)', en: 'start_gid (optional)' },
  'admin.task.startGidHint':     { 'zh-CN': '留空从最新开始', 'zh-TW': '留空從最新開始', en: 'Leave empty to start from latest' },
  'admin.task.intervalHours':    { 'zh-CN': 'run_interval_hours (同步间隔/小时)', 'zh-TW': 'run_interval_hours (同步間隔/小時)', en: 'run_interval_hours (sync interval/hrs)' },
  'admin.task.existsIncremental':{ 'zh-CN': '已存在 gallery incremental sync。系统仅允许一个。', 'zh-TW': '已存在 gallery incremental sync。系統僅允許一個。', en: 'A gallery incremental sync already exists. Only one is allowed.' },
  'admin.task.existsFavorites':  { 'zh-CN': '已存在 favorites source sync。系统仅允许一个。', 'zh-TW': '已存在 favorites source sync。系統僅允許一個。', en: 'A favorites source sync already exists. Only one is allowed.' },
  'admin.task.cancel':           { 'zh-CN': '取消', 'zh-TW': '取消', en: 'Cancel' },
  'admin.task.create':           { 'zh-CN': '创建任务', 'zh-TW': '建立任務', en: 'Create Task' },

  // Admin — delete confirm
  'admin.task.deleteConfirm':    { 'zh-CN': '确认删除任务', 'zh-TW': '確認刪除任務', en: 'Confirm Delete Task' },
  'admin.task.deleteHint':       { 'zh-CN': '请输入任务名以确认删除：', 'zh-TW': '請輸入任務名以確認刪除：', en: 'Type the task name to confirm deletion:' },
  'admin.task.deleteInputAria':  { 'zh-CN': '输入任务名以确认', 'zh-TW': '輸入任務名以確認', en: 'Enter task name to confirm' },
  'admin.task.deleteBtn':        { 'zh-CN': '删除', 'zh-TW': '刪除', en: 'Delete' },

  // Admin — sync runs
  'admin.task.empty':            { 'zh-CN': '暂无任务，点击「新建任务」开始', 'zh-TW': '暫無任務，點擊「新建任務」開始', en: 'No tasks yet. Click "New Task" to start.' },

  // Admin — similarity distribution
  'admin.sim.vocab':             { 'zh-CN': '词表', 'zh-TW': '詞表', en: 'Vocab' },
  'admin.sim.embedded':          { 'zh-CN': '已嵌入', 'zh-TW': '已嵌入', en: 'Embedded' },
  'admin.sim.pending':           { 'zh-CN': '待处理', 'zh-TW': '待處理', en: 'Pending' },
  'admin.sim.profileLiked':      { 'zh-CN': '偏好画廊', 'zh-TW': '偏好畫廊', en: 'Profile liked' },
  'admin.sim.noData':            { 'zh-CN': '暂无相似度数据，请先运行 Favorites Sync，等待词表构建和向量化完成', 'zh-TW': '暫無相似度資料，請先執行 Favorites Sync，等待詞表建構和向量化完成', en: 'No similarity data yet. Run Favorites Sync first, then wait for vocab build and vectorization.' },
  'admin.sim.candidates':        { 'zh-CN': '候选总数', 'zh-TW': '候選總數', en: 'Candidates' },
  'admin.sim.aboveThreshold':    { 'zh-CN': '相似度 ≥ {threshold}', 'zh-TW': '相似度 ≥ {threshold}', en: 'Similarity ≥ {threshold}' },
  'admin.sim.saveThreshold':     { 'zh-CN': '保存阈值 {threshold}', 'zh-TW': '儲存閾值 {threshold}', en: 'Save threshold {threshold}' },
  'admin.sim.histogram':         { 'zh-CN': '相似度分布直方图', 'zh-TW': '相似度分佈直方圖', en: 'Similarity distribution histogram' },
  'admin.sim.threshold':         { 'zh-CN': '阈值', 'zh-TW': '閾值', en: 'Threshold' },
  'admin.sim.thresholdValue':    { 'zh-CN': '阈值数值', 'zh-TW': '閾值數值', en: 'Threshold value' },

  // Admin — thumb queue
  'admin.queue.waitingInfo':     { 'zh-CN': '失败重试等待中，冷却后进入 Pending', 'zh-TW': '失敗重試等待中，冷卻後進入 Pending', en: 'Waiting for retry cooldown before entering Pending' },
};

export function t(key, vars) {
  const entry = T[key];
  if (!entry) return key;
  let text = entry[locale] || entry.en || key;
  if (vars) {
    for (const [k, v] of Object.entries(vars)) {
      text = text.replace(`{${k}}`, String(v));
    }
  }
  return text;
}

export function formatDate(value, opts = { year: 'numeric', month: '2-digit', day: '2-digit' }) {
  if (!value) return null;
  const intl = INTL_LOCALE[locale] || 'en-US';
  return new Date(value).toLocaleDateString(intl, opts);
}

export function formatNumber(value) {
  if (value === null || value === undefined) return '';
  const intl = INTL_LOCALE[locale] || 'en-US';
  return Number(value).toLocaleString(intl);
}
