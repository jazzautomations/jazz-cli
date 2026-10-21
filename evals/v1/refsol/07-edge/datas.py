def bissexto(a):
    return a%4==0 and (a%100!=0 or a%400==0)
