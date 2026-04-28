import sqlite3

try:
    conn = sqlite3.connect('D:\\dpProject\\playbot\\workspace\\data.db')
    cursor = conn.cursor()
    cursor.execute("SELECT * FROM app_settings;")
    
    # Get column names
    names = [description[0] for description in cursor.description]
    print("Columns:", names)
    
    rows = cursor.fetchall()
    for row in rows:
        print(row)
except Exception as e:
    print("Error:", e)
