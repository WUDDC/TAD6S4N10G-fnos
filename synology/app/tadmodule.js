// TAD6S4N 模块 —— DSM 桌面应用（SPM 同机制：SYNO.SDS 框架注册）。
// 窗口内容是 iframe，指向本套件经 DSM nginx 反代的页面
// /webman/3rdparty/tad-module/（与 DSM 同源，登录态自动携带，
// 后端 CSP frame-ancestors 'self' 允许同源内嵌）。
// 注意：类名命名空间用 tadmodule（JS 标识符安全），与套件名 tad-module 无关。
Ext.ns("com.wuddc.packages.tadmodule");

com.wuddc.packages.tadmodule.AppInstance = Ext.extend(SYNO.SDS.AppInstance, {
    appWindowName: "com.wuddc.packages.tadmodule.AppWindow",
    constructor: function () {
        com.wuddc.packages.tadmodule.AppInstance.superclass.constructor.call(this, arguments);
    }
});

com.wuddc.packages.tadmodule.AppWindow = Ext.extend(SYNO.SDS.AppWindow, {
    constructor: function () {
        var config = {
            width: 980,
            height: 640,
            resizable: true,
            maximizable: true,
            minimizable: true,
            layout: "fit",
            bodyStyle: "padding:0",
            items: [{
                xtype: "box",
                autoEl: {
                    tag: "iframe",
                    src: "/webman/3rdparty/tad-module/",
                    frameborder: "0",
                    style: "width:100%;height:100%;border:0;display:block;background:#f3f4f6"
                }
            }]
        };
        com.wuddc.packages.tadmodule.AppWindow.superclass.constructor.call(this, config);
    }
});
