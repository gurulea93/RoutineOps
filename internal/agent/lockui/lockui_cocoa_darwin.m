// Cocoa-реализация полноэкранного замка блокировки (macOS). Компилируется только
// на darwin при включённом CGO. Колбэк проверки пароля — экспортированная Go-функция
// lockuiVerify (1 = верно → закрыть замок).
#import <Cocoa/Cocoa.h>
#import <IOKit/pwr_mgt/IOPMLib.h>

extern int lockuiVerify(char* pw);

// Borderless-окна по умолчанию НЕ становятся key window (canBecomeKeyWindow==NO),
// поэтому без переопределения makeKeyAndOrderFront: их ставит поверх, но клавиатурный
// ввод в них не маршрутизируется — текстовое поле визуально есть, но неактивно.
@interface MDMKeyWindow : NSWindow
@end

@implementation MDMKeyWindow
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return YES; }
@end

@interface MDMLockController : NSObject
@property (strong) NSSecureTextField* field;
@property (strong) NSTextField* errorLabel;
@property IOPMAssertionID assertionID;
- (void)submit:(id)sender;
@end

@implementation MDMLockController
- (void)submit:(id)sender {
    const char* pw = [[self.field stringValue] UTF8String];
    if (pw != NULL && lockuiVerify((char*)pw) == 1) {
        if (self.assertionID != kIOPMNullAssertionID) {
            IOPMAssertionRelease(self.assertionID);
        }
        [NSApp stop:nil];
        // stop вступает в силу после следующего события — пнём вручную.
        NSEvent* ev = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
            location:NSZeroPoint modifierFlags:0 timestamp:0 windowNumber:0
            context:nil subtype:0 data1:0 data2:0];
        [NSApp postEvent:ev atStart:YES];
        return;
    }
    [self.errorLabel setStringValue:@"Неверный пароль"];
    [self.field setStringValue:@""];
}
@end

static NSTextField* makeLabel(NSRect frame, NSString* text, NSColor* color, NSFont* font) {
    NSTextField* l = [[NSTextField alloc] initWithFrame:frame];
    [l setStringValue:text];
    [l setBezeled:NO];
    [l setEditable:NO];
    [l setSelectable:NO];
    [l setDrawsBackground:NO];
    [l setAlignment:NSTextAlignmentCenter];
    [l setTextColor:color];
    if (font != nil) { [l setFont:font]; }
    return l;
}

void lockui_show(const char* reason) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        MDMLockController* ctrl = [[MDMLockController alloc] init];
        
        // Prevent display sleep
        IOPMAssertionID assertionID = kIOPMNullAssertionID;
        IOReturn success = IOPMAssertionCreateWithName(
            kIOPMAssertionTypePreventUserIdleDisplaySleep,
            kIOPMAssertionLevelOn,
            CFSTR("MDM Lock Screen"),
            &assertionID);
        if (success == kIOReturnSuccess) {
            ctrl.assertionID = assertionID;
        }

        NSRect frame = [[NSScreen mainScreen] frame];
        CGFloat cx = frame.size.width / 2.0;
        CGFloat cy = frame.size.height / 2.0;

        MDMKeyWindow* win = [[MDMKeyWindow alloc] initWithContentRect:frame
            styleMask:NSWindowStyleMaskBorderless
            backing:NSBackingStoreBuffered defer:NO];
        [win setLevel:NSScreenSaverWindowLevel]; // Show above everything, including screen saver
        [win setBackgroundColor:[NSColor colorWithCalibratedWhite:0.10 alpha:1.0]];
        [win setCollectionBehavior:NSWindowCollectionBehaviorCanJoinAllSpaces
            | NSWindowCollectionBehaviorFullScreenAuxiliary];

        NSView* content = [win contentView];

        [content addSubview:makeLabel(NSMakeRect(cx - 350, cy + 70, 700, 50),
            @"Устройство заблокировано", [NSColor whiteColor], [NSFont boldSystemFontOfSize:30])];
        [content addSubview:makeLabel(NSMakeRect(cx - 350, cy + 30, 700, 30),
            [NSString stringWithUTF8String:reason], [NSColor whiteColor], nil)];

        NSSecureTextField* field = [[NSSecureTextField alloc]
            initWithFrame:NSMakeRect(cx - 160, cy - 20, 220, 26)];
        ctrl.field = field;
        [field setTarget:ctrl];
        [field setAction:@selector(submit:)]; // Enter = разблокировать
        [content addSubview:field];

        NSButton* btn = [[NSButton alloc] initWithFrame:NSMakeRect(cx + 70, cy - 22, 130, 30)];
        [btn setTitle:@"Разблокировать"];
        [btn setBezelStyle:NSBezelStyleRounded];
        [btn setTarget:ctrl];
        [btn setAction:@selector(submit:)];
        [content addSubview:btn];

        NSTextField* err = makeLabel(NSMakeRect(cx - 160, cy - 60, 320, 24),
            @"", [NSColor systemRedColor], nil);
        ctrl.errorLabel = err;
        [content addSubview:err];

        // Порядок важен: процесс — accessory (без Dock-иконки), для приёма клавиатуры
        // его сначала нужно активировать, и только потом делать окно key/first responder.
        [NSApp activateIgnoringOtherApps:YES];
        [win makeKeyAndOrderFront:nil];
        [win makeFirstResponder:field];

        // Disable Apple menu and other elements if possible
        NSMenu *mainMenu = [[NSMenu alloc] initWithTitle:@"MainMenu"];
        [NSApp setMainMenu:mainMenu];

        // Ensure window covers all screens if multiple
        for (NSScreen *screen in [NSScreen screens]) {
            if (screen != [NSScreen mainScreen]) {
                NSWindow *auxWin = [[NSWindow alloc] initWithContentRect:[screen frame]
                                                                styleMask:NSWindowStyleMaskBorderless
                                                                  backing:NSBackingStoreBuffered
                                                                    defer:NO];
                [auxWin setLevel:NSScreenSaverWindowLevel];
                [auxWin setBackgroundColor:[NSColor colorWithCalibratedWhite:0.10 alpha:1.0]];
                [auxWin setCollectionBehavior:NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary];
                [auxWin makeKeyAndOrderFront:nil];
            }
        }
        
        [NSApp run];
    }
}
